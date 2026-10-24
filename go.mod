module github.com/jevlinai/jevlin-go

go 1.27.0

toolchain go1.27.3

// Go 1.27 makes SSL_CERT_FILE and SSL_CERT_DIR replace the platform
// certificate store on macOS and Windows. Keep the platform store, as
// before: a participant's SSL_CERT_FILE set for another tool must not
// change which roots the router and AS connections trust. Go plans to
// remove the setting in 1.31; this line must be decided again then.
godebug x509sslcertoverrideplatform=0

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/conductorone/dpop v0.2.7
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/oauth2 v0.36.0
	golang.org/x/sys v0.47.0
	golang.org/x/term v0.45.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jellydator/ttlcache/v3 v3.3.0 // indirect
	golang.org/x/sync v0.11.0 // indirect
)
