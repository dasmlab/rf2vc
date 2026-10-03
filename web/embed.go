package web

import "embed"

//go:embed index.html static/*
var Assets embed.FS

// OAuthTemplates are oauth-proxy --custom-templates-dir files (sign_in.html, error.html).
//
//go:embed oauth/*.html
var OAuthTemplates embed.FS
