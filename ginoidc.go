package gin_oidc

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc"
	sessions "github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type InitParams struct {
	ClientId      string          //id from the authorization service (OIDC provider)
	ClientSecret  string          //secret from the authorization service (OIDC provider)
	ClientName    string          // a name for the Client, becomes a base path
	Issuer        url.URL         //the URL identifier for the authorization service. for example: "https://accounts.google.com" - try adding "/.well-known/openid-configuration" to the path to make sure it's correct
	ClientUrl     url.URL         //your website's/service's URL for example: "http://localhost:8081/" or "https://mydomain.com/
	Scopes        []string        //OAuth scopes. If you're unsure go with: []string{oidc.ScopeOpenID, "profile", "email"}
	ErrorHandler  gin.HandlerFunc //errors handler. for example: func(c *gin.Context) {c.String(http.StatusBadRequest, "ERROR...")}
	CallbackPath  string
	LogoutUrl     *url.URL // the logout URL at the Issuer to connect to for ending the session.  Is set automatically.
	PostLogoutUrl url.URL  //user will be redirected to this URL after he logs out (i.e. accesses the '/logout' endpoint added in 'Init()')
}

type OidcMiddleware struct {
	InitParams
	Verifier        *oidc.IDTokenVerifier
	Config          *oauth2.Config
	AuthHandler     gin.HandlerFunc
	LogoutHandler   gin.HandlerFunc
	CallbackHandler gin.HandlerFunc
}

func New(i InitParams) *OidcMiddleware {
	verifier, config := initVerifierAndConfig(&i)
	authHandler := protectMiddleware(i, verifier, config)
	logoutHandler := logoutHandler(i)
	callbackHandler := callbackHandler(i, verifier, config)

	return &OidcMiddleware{i, verifier, config, authHandler, logoutHandler, callbackHandler}
}

func initVerifierAndConfig(i *InitParams) (*oidc.IDTokenVerifier, *oauth2.Config) {
	providerCtx := context.Background()
	provider, err := oidc.NewProvider(providerCtx, i.Issuer.String())
	if err != nil {
		log.Fatalf("Failed to init OIDC provider. Error: %v \n", err.Error())
	}
	oidcConfig := &oidc.Config{
		ClientID: i.ClientId,
	}
	verifier := provider.Verifier(oidcConfig)
	endpoint := provider.Endpoint()
	i.ClientUrl.Path = i.CallbackPath
	config := &oauth2.Config{
		ClientID:     i.ClientId,
		ClientSecret: i.ClientSecret,
		Endpoint:     endpoint,
		RedirectURL:  i.ClientUrl.String(),
		Scopes:       i.Scopes,
	}

	issuerMetadata := make(map[string]interface{})
	err = provider.Claims(&issuerMetadata)
	if err != nil {
		log.Fatalf("Failed to parse Issuer (%v) discovery endpoint.  Error: %v\n", i.Issuer, err)
	}
	if logout, ok := issuerMetadata["end_session_endpoint"]; ok {
		logoutUrl, err := url.Parse(logout.(string))
		if err != nil {
			log.Fatalf("Funny business getting end_session_endpoint for Issuer %v. Got URL: %v Error: %v\n", i.Issuer, logout, err)
		}
		i.LogoutUrl = logoutUrl
	} else {
		logoutUrl, err := url.Parse(strings.TrimSuffix(i.Issuer.String(), "/") + "protocol/openid-connect/logout")
		if err != nil {
			log.Fatalf("Could not set default logout for Issuer %v. Error: %v\n", i.Issuer, err)
		}
		i.LogoutUrl = logoutUrl
	}
	return verifier, config
}

func logoutHandler(i InitParams) func(c *gin.Context) {
	return func(c *gin.Context) {
		serverSession := sessions.Default(c)

		rawIDToken := serverSession.Get("oidcIDToken")
		var skipProviderLogout = true
		if rawIDToken != nil && rawIDToken.(string) != "" {
			skipProviderLogout = false
		}

		serverSession.Set("oidcAuthorized", false)
		serverSession.Set("oidcClaims", nil)
		serverSession.Set("oidcState", nil)
		serverSession.Set("oidcOriginalRequestUrl", nil)
		serverSession.Set("oidcIDToken", nil)
		serverSession.Save()

		if skipProviderLogout {
			c.Redirect(http.StatusFound, i.PostLogoutUrl.String())
			return
		}

		logoutUrl := *i.LogoutUrl
		//XXX This may need to be different depending on "provider" (i.e. Okta vs. Auth0)

		logoutUrl.RawQuery = (url.Values{
			"post_logout_redirect_uri": []string{i.PostLogoutUrl.String()},
			"id_token_hint":            []string{rawIDToken.(string)}}).Encode()
		c.Redirect(http.StatusFound, logoutUrl.String())
	}
}

func callbackHandler(i InitParams, verifier *oidc.IDTokenVerifier, config *oauth2.Config) func(c *gin.Context) {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		serverSession := sessions.Default(c)

		state, ok := (serverSession.Get("oidcState")).(string)
		if handleOk(c, i, ok, "failed to parse state") {
			return
		}

		if handleOk(c, i, c.Query("state") == state, "get 'state' param didn't match local 'state' value") {
			return
		}

		oauth2Token, err := config.Exchange(ctx, c.Query("code"))
		if handleError(c, i, err, "failed to exchange token") {
			return
		}

		rawIDToken, ok := oauth2Token.Extra("id_token").(string)
		if handleOk(c, i, ok, "no id_token field in oauth2 token") {
			return
		}

		idToken, err := verifier.Verify(ctx, rawIDToken)
		if handleError(c, i, err, "failed to verify id token") {
			return
		}

		var claims map[string]interface{}
		err = idToken.Claims(&claims)
		if handleError(c, i, err, "failed to parse id token") {
			return
		}

		claimsJson, err := json.Marshal(claims)
		if handleError(c, i, err, "failed to marshal id token: ") {
			return
		}

		originalRequestUrl, ok := (serverSession.Get("oidcOriginalRequestUrl")).(string)
		if handleOk(c, i, ok, "failed to parse originalRequestUrl") {
			return
		}

		serverSession.Set("oidcAuthorized", true)
		serverSession.Set("oidcState", nil)
		serverSession.Set("oidcOriginalRequestUrl", nil)
		serverSession.Set("oidcClaims", string(claimsJson))
		serverSession.Set("oidcIDToken", rawIDToken)

		// if we were using cookies for session state then we don't want to store
		// these with the browser.  Adding them will also make the whole thing way
		// to big and we'll likely see errors.
		// serverSession.Set("oidcAccessToken", oauth2Token.AccessToken)
		// serverSession.Set("oidcRefreshToken", oauth2Token.RefreshToken)

		err = serverSession.Save()
		if handleError(c, i, err, "failed save sessions.") {
			return
		}

		c.Redirect(http.StatusFound, originalRequestUrl)
	}
}

func protectMiddleware(i InitParams, verifier *oidc.IDTokenVerifier, config *oauth2.Config) func(c *gin.Context) {
	return func(c *gin.Context) {
		serverSession := sessions.Default(c)
		authorized := serverSession.Get("oidcAuthorized")
		if (authorized != nil && authorized.(bool)) ||
			c.Request.URL.Path == i.CallbackPath {

			rawIDToken := serverSession.Get("oidcIDToken")
			if rawIDToken != nil && rawIDToken.(string) != "" {
				ctx := c.Request.Context()
				_, err := verifier.Verify(ctx, rawIDToken.(string))
				if handleError(c, i, err, "failed to verify id token") {
					return
				}
			}
			c.Next()
			return
		}
		state := RandomString(16)
		serverSession.Set("oidcAuthorized", false)
		serverSession.Set("oidcState", state)
		serverSession.Set("oidcOriginalRequestUrl", c.Request.URL.String())
		serverSession.Set("oidcIDToken", nil)
		err := serverSession.Save()
		if err != nil {
			log.Fatal("failed save sessions. error: " + err.Error()) // todo handle more gracefully
		}
		c.Redirect(http.StatusFound, config.AuthCodeURL(state)) //redirect to authorization server
	}

}

func handleError(c *gin.Context, i InitParams, err error, message string) bool {
	if err == nil {
		return false
	}
	c.Error(errors.New(message + " [" + err.Error() + "]"))
	i.ErrorHandler(c)
	c.Abort()
	return true
}

func handleOk(c *gin.Context, i InitParams, ok bool, message string) bool {
	if ok {
		return false
	}
	return handleError(c, i, errors.New("not ok"), message)
}

//random string
var src = rand.NewSource(time.Now().UnixNano())

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const (
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
)

func RandomString(n int) string {
	b := make([]byte, n)
	for i, cache, remain := n-1, src.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = src.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			b[i] = letterBytes[idx]
			i--
		}
		cache >>= letterIdxBits
		remain--
	}

	return string(b)
}
