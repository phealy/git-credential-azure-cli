# git-credential-azure-cli

A Git credential helper that uses Azure CLI to obtain OAuth tokens for Azure DevOps, Visual Studio Team Services, and Go module proxies.

## Prerequisites

- [Azure CLI](https://docs.microsoft.com/en-us/cli/azure/install-azure-cli) installed and authenticated (`az login`)

## Installation

### Option 1: Build from source (Go)

```bash
go build -o git-credential-azure-cli .
cp git-credential-azure-cli /usr/local/bin/
```

### Option 2: Use Python script

Requires Python 3.6+

```bash
cp git-credential-azure-cli.py /usr/local/bin/git-credential-azure-cli
chmod +x /usr/local/bin/git-credential-azure-cli
```

Then configure Git to use the helper.

## Configuration

### Credential Caching (Recommended)

Add the cache helper first to prevent Entra ID rate limiting. The helper provides `password_expiry_utc` so the cache knows when to refresh:

```bash
git config --global --replace-all credential.helper cache
git config --global --add credential.helper git-credential-azure-cli
```

### Global Helper

Configure the helper globally with domain filtering:

```bash
git config --global credential.helper git-credential-azure-cli
```

By default, this handles hosts ending with `visualstudio.com`, `dev.azure.com`, or `goproxyprod.goms.io`.

### GOPROXY Authentication

This helper can also be used for Go module proxy authentication via the `GOAUTH` environment variable:

```bash
export GOAUTH="git /"
```

This tells Go to use the git credential system for authentication, which will invoke this helper for matching domains. `/` can be used as the directory because the credential helper is in the user's 
global configuration. If configured in a specific directory, use that path instead.

### Allowed Domains

Set which domains the helper should process (comma-separated). Uses "ends with" matching, so `visualstudio.com` matches `msazure.visualstudio.com`:

```bash
git config --global credential.azureCliHelper.allowedDomains "visualstudio.com,dev.azure.com"
```

Default: `visualstudio.com,dev.azure.com,goproxyprod.goms.io`

## How It Works

1. When Git needs credentials, it calls this helper with credential information including the host and any WWW-Authenticate headers.

2. The helper checks if:
   - The protocol is HTTPS
   - The host matches one of the allowed domains

3. It attempts to get an OAuth token from Azure CLI:
   - If the host has a resource override (e.g., `goproxyprod.goms.io` uses `https://microsoft.onmicrosoft.com/AKSGoProxyMSFT`), uses that resource
   - Otherwise tries: `az account get-access-token --resource https://<host>/`
   - If that fails and a `realm` is present in the WWW-Authenticate headers, tries that realm as the resource

4. If a token is obtained, it outputs credentials in the format Git expects:
   ```
   authtype=bearer
   username=null
   password=<accessToken>
   password_expiry_utc=<unix_timestamp>
   ```

## Troubleshooting

### Verify Azure CLI is authenticated

```bash
az account show
```

### Test the helper manually

```bash
echo -e "protocol=https\nhost=msazure.visualstudio.com\n" | git-credential-azure-cli get
```

### Check allowed domains configuration

```bash
git config --get credential.azureCliHelper.allowedDomains
```

## License

MIT
