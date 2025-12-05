# git-credential-azure-cli

A Git credential helper that uses Azure CLI to obtain OAuth tokens for Azure DevOps, Visual Studio Team Services, and Go module proxies.

## Prerequisites

- [Azure CLI](https://docs.microsoft.com/en-us/cli/azure/install-azure-cli) installed and authenticated (`az login`)
- Go 1.21+ (for building from source)

## Installation

```bash
go build -o git-credential-azure-cli .
sudo cp git-credential-azure-cli /usr/local/bin/
```

Or use the `init` command after building:

```bash
git-credential-azure-cli init
```

This will configure Git to use the cache helper (to prevent rate limiting) and add this tool as a credential helper.

## Configuration

### Quick Setup

The easiest way to configure is using the `init` command:

```bash
git-credential-azure-cli init
```

### Manual Setup

Add the cache helper first to prevent Entra ID rate limiting. The helper provides `password_expiry_utc` so the cache knows when to refresh:

```bash
git config --global --replace-all credential.helper cache
git config --global --add credential.helper /path/to/git-credential-azure-cli
```

### Allowed Domains

Set which domains the helper should process. Uses "ends with" matching, so `visualstudio.com` matches `msazure.visualstudio.com`:

```bash
git config --global --add azureCliCredentialHelper.allowedDomain "visualstudio.com"
git config --global --add azureCliCredentialHelper.allowedDomain "dev.azure.com"
```

Default: `visualstudio.com`, `dev.azure.com`

### Resource Overrides

For hosts that need a different token resource (e.g., Go module proxies):

```bash
git config --global "azureCliCredentialHelper.https://mydomain.com.resource" "https://myoauth2resourceURL"
```

### GOAUTH Authentication

This helper can be used for Go module proxy authentication via the `GOAUTH` environment variable:

```bash
eval "$(git-credential-azure-cli exports)"
```

Or add to your shell profile:

```bash
git-credential-azure-cli exports >> ~/.bashrc
```

This sets `GOAUTH` to use the git credential system for authentication.

## How It Works

1. When Git needs credentials, it calls this helper with credential information including the host and any WWW-Authenticate headers.

2. The helper checks if:
   - The protocol is HTTPS
   - The host matches one of the allowed domains

3. It attempts to get an OAuth token from Azure CLI:
   - If the host has a resource override configured, uses that resource
   - Otherwise constructs the resource from the host URL
   - If that fails and a `realm` is present in the WWW-Authenticate headers, tries that realm as the resource

4. If a token is obtained, it outputs credentials in the format Git expects:
   ```
   authtype=bearer
   username=null
   password=<accessToken>
   password_expiry_utc=<unix_timestamp>
   ```

## Commands

- `init` - Configure git credential helpers
- `exports` - Output environment variable exports for GOAUTH
- `get` - Get credentials (called by git automatically)
- `store` - No-op (credentials managed by Azure CLI)
- `erase` - No-op (credentials managed by Azure CLI)

Use `-v`, `-vv`, or `-vvv` for increasing verbosity levels.

## Troubleshooting

### Verify Azure CLI is authenticated

```bash
az account show
```

### Test the helper manually

```bash
echo -e "protocol=https\nhost=dev.azure.com\n" | git-credential-azure-cli get
```

### Debug mode

```bash
echo -e "protocol=https\nhost=dev.azure.com\n" | git-credential-azure-cli -vvv get
```

### Check configuration

```bash
git config --global --get-all azureCliCredentialHelper.allowedDomain
```

## License

MIT
