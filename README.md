# gin-oidc
gin middleware - an OIDC client for a relying party web server

# example usage

```golang
  	// middleware params
 	initParams := gin_oidc.InitParams{
 		ClientId:     "xx-xxx-xxx",
 		ClientSecret: "xx-xxx-xxx",
 		Issuer:       "https://accounts.google.com/", //add '.well-known/openid-configuration' to see it's a good link
 		ClientUrl:    "http://example.domain/", //your website's url
		// "openid" is a required scope for OpenID Connect flows.
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
 		ErrorHandler: func(c *gin.Context) {
 			// gin_oidc pushes a new error before any "ErrorHandler" invocation
 			message := c.Errors.Last().Error()
 			// redirect to ErrorEndpoint with error message
 			redirectToErrorPage(c, "http://example2.domain/error", message)
 			// when "ErrorHandler" ends "c.Abort()" is invoked - no further handlers will be invoked
		},
		CallbackPath: "oidc-callback", // redirect url path
 		PostLogoutUrl: "http://example2.domain/",
 	}
	currOIDC := gin_oidc.New(initParams)

	// configure route
	router.GET("/logout", currOIDC.LogoutHandler)
	router.Any("/"+currOIDC.CallbackPath, currOIDC.CallbackHandler)

 	// protect all endpoint below this line
	router.Use(currOIDC.AuthHandler)

	// or protect a single endpoint
	router.GET("/protectedEndpoint", currOIDC.AuthHandler, protectedEndpointHandler)
  
```



If you have any questions feel free to open an issue.
