# Keycloak provisioning (pending)

The realm export that provisions the `jungle-gaming` realm, the
`wagering-api` audience, provider `client_credentials` clients, and test
identities will be added here as `realm-export.json` once the
authentication piece of the challenge is built.

Once added, `docker-compose.yml` mounts this directory read-only into the
Keycloak container and starts it with `--import-realm` so a clean checkout
gets a fully provisioned IdP with no manual console steps. Until then,
Keycloak runs in plain `start-dev` mode.
