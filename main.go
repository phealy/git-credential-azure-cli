// Git credential helper that uses Azure CLI credentials to get OAuth tokens.
//
// Usage:
//
//	# Initialize git configuration:
//	git-credential-azure-cli init
//
//	# Show environment exports for GOAUTH:
//	git-credential-azure-cli exports
//
//	# The credential helper is invoked by git automatically:
//	git config --global --replace-all credential.helper cache
//	git config --global --add credential.helper /path/to/git-credential-azure-cli
//
// Configuration:
//
//	# Set allowed domains (can be specified multiple times, uses "ends with" matching):
//	git config --global --add azureCliCredentialHelper.allowedDomain "visualstudio.com"
//	git config --global --add azureCliCredentialHelper.allowedDomain "dev.azure.com"
//
//	# Set resource overrides for specific URLs (uses git's urlmatch):
//	git config --global "azureCliCredentialHelper.https://yourproxy.yourdomain.resource" "https://microsoft.onmicrosoft.com/AKSGoProxyMSFT"
//	# Query with: git config --get-urlmatch azureCliCredentialHelper https://yourproxy.yourdomain
//
//	# Set tenant overrides for specific URLs (uses git's urlmatch):
//	git config --global "azureCliCredentialHelper.https://yourproxy.yourdomain.tenant" "your-tenant-id-or-name"
//	# Query with: git config --get-urlmatch azureCliCredentialHelper https://yourproxy.yourdomain
//
//	# Default allowed domains: visualstudio.com,dev.azure.com
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/gopasspw/gitconfig"
	"github.com/spf13/cobra"
)

// Version information (set via ldflags at build time)
var version = "dev"

var defaultAllowedDomains = []string{"visualstudio.com", "dev.azure.com"}

// Resource overrides for hosts that need a different token resource.
// Configured via git config "azureCliCredentialHelper.<url>.resource" "<resourceURL>"
var defaultResourceOverrides = map[string]string{}

// Cached config values
var (
	gitCfg            *gitconfig.Configs
	allowedDomains    []string
	resourceOverrides map[string]string
	tenantOverrides   map[string]string
)

// Verbose level for debug output
var verbosity int

func debugf(level int, format string, args ...interface{}) {
	if verbosity >= level {
		fmt.Fprintf(os.Stderr, "[DEBUG] "+format+"\n", args...)
	}
}

func loadConfig() {
	debugf(2, "Loading git configuration")
	gitCfg = gitconfig.New()
	gitCfg.LoadAll("")

	// Load allowed domains (supports multiple values via --add)
	// Git stores keys lowercase, so we use the lowercase version
	domains := gitCfg.GetAll("azureclicredentialhelper.alloweddomain")
	if len(domains) == 0 {
		allowedDomains = defaultAllowedDomains
		debugf(2, "Using default allowed domains: %v", allowedDomains)
	} else {
		for _, d := range domains {
			d = strings.TrimSpace(d)
			if d != "" {
				allowedDomains = append(allowedDomains, d)
			}
		}
		if len(allowedDomains) == 0 {
			allowedDomains = defaultAllowedDomains
		}
		debugf(2, "Loaded allowed domains from config: %v", allowedDomains)
	}

	// Load resource overrides
	// Keys are in format: azureclicredentialhelper.<url>.resource
	resourceOverrides = make(map[string]string)
	for k, v := range defaultResourceOverrides {
		resourceOverrides[k] = v
	}

	// Load tenant overrides
	// Keys are in format: azureclicredentialhelper.<url>.tenant
	tenantOverrides = make(map[string]string)

	const prefix = "azureclicredentialhelper."
	const resourceSuffix = ".resource"
	const tenantSuffix = ".tenant"
	for _, key := range gitCfg.List(prefix) {
		if strings.HasSuffix(key, resourceSuffix) {
			// Extract URL/host between prefix and suffix
			urlPart := strings.TrimPrefix(key, prefix)
			urlPart = strings.TrimSuffix(urlPart, resourceSuffix)
			if urlPart != "" {
				if resource := gitCfg.Get(key); resource != "" {
					resourceOverrides[urlPart] = resource
					debugf(2, "Loaded resource override: %s -> %s", urlPart, resource)
				}
			}
		} else if strings.HasSuffix(key, tenantSuffix) {
			// Extract URL/host between prefix and suffix
			urlPart := strings.TrimPrefix(key, prefix)
			urlPart = strings.TrimSuffix(urlPart, tenantSuffix)
			if urlPart != "" {
				if tenant := gitCfg.Get(key); tenant != "" {
					tenantOverrides[urlPart] = tenant
					debugf(2, "Loaded tenant override: %s -> %s", urlPart, tenant)
				}
			}
		}
	}
}

func isAllowedHost(host string, allowedDomains []string) bool {
	host = strings.ToLower(host)
	for _, domain := range allowedDomains {
		domain = strings.ToLower(domain)
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func getResourceForHost(protocol, host string) string {
	url := fmt.Sprintf("%s://%s", protocol, host)
	// Check for URL-based override first (e.g., https://yourproxy.yourdomain)
	if resource, ok := resourceOverrides[url]; ok {
		return resource
	}
	// Check for host-only override (e.g., yourproxy.yourdomain)
	if resource, ok := resourceOverrides[host]; ok {
		return resource
	}
	return url + "/"
}

func getTenantForHost(protocol, host string) string {
	url := fmt.Sprintf("%s://%s", protocol, host)
	// Check for URL-based override first (e.g., https://yourproxy.yourdomain)
	if tenant, ok := tenantOverrides[url]; ok {
		return tenant
	}
	// Check for host-only override (e.g., yourproxy.yourdomain)
	if tenant, ok := tenantOverrides[host]; ok {
		return tenant
	}
	return ""
}

func parseInput() (map[string]string, []string) {
	data := make(map[string]string)
	var wwwauth []string

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			break
		}

		if idx := strings.Index(line, "="); idx != -1 {
			key := line[:idx]
			value := line[idx+1:]
			if key == "wwwauth[]" {
				wwwauth = append(wwwauth, value)
			} else {
				data[key] = value
			}
			debugf(3, "Parsed input: %s=%s", key, value)
		}
	}

	return data, wwwauth
}

func extractRealm(wwwauthEntries []string) string {
	re := regexp.MustCompile(`realm="([^"]+)"`)
	for _, entry := range wwwauthEntries {
		matches := re.FindStringSubmatch(entry)
		if len(matches) > 1 {
			return matches[1]
		}
	}
	return ""
}

func getAccessToken(ctx context.Context, cred *azidentity.AzureCLICredential, resource string) (string, int64, error) {
	// Convert resource to scope format (.default suffix)
	scope := resource
	if !strings.HasSuffix(scope, "/") {
		scope = scope + "/"
	}
	scope = scope + ".default"

	debugf(2, "Requesting token for scope: %s", scope)

	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{scope},
	})
	if err != nil {
		debugf(1, "Failed to get token: %v", err)
		return "", 0, err
	}

	debugf(2, "Token acquired, expires at: %v", token.ExpiresOn)
	return token.Token, token.ExpiresOn.Unix(), nil
}

func outputCredential(accessToken string, expiryUTC int64) {
	fmt.Println("authtype=bearer")
	fmt.Println("username=null")
	fmt.Printf("password=%s\n", accessToken)
	if expiryUTC > 0 {
		fmt.Printf("password_expiry_utc=%d\n", expiryUTC)
	}
}

func getCredential(cmd *cobra.Command, args []string) {
	// Load configuration
	loadConfig()

	data, wwwauth := parseInput()

	protocol := data["protocol"]
	host := data["host"]

	debugf(1, "Handling get request for %s://%s", protocol, host)

	// Only handle HTTPS
	if protocol != "https" {
		debugf(1, "Skipping non-HTTPS protocol: %s", protocol)
		return
	}

	// Check if host is in allowed domains
	if !isAllowedHost(host, allowedDomains) {
		debugf(1, "Host not in allowed domains: %s", host)
		return
	}

	// Create Azure CLI credential with optional tenant override
	ctx := context.Background()
	tenant := getTenantForHost(protocol, host)
	var credOpts *azidentity.AzureCLICredentialOptions
	if tenant != "" {
		debugf(1, "Using tenant override: %s", tenant)
		credOpts = &azidentity.AzureCLICredentialOptions{
			TenantID: tenant,
		}
	}
	cred, err := azidentity.NewAzureCLICredential(credOpts)
	if err != nil {
		debugf(1, "Failed to create Azure CLI credential: %v", err)
		os.Exit(1)
	}

	// Try getting token for the host (using override if available)
	resource := getResourceForHost(protocol, host)
	debugf(1, "Using resource: %s", resource)
	accessToken, expiryUTC, err := getAccessToken(ctx, cred, resource)

	// If that fails and no override was used, try using the realm from wwwauth
	if err != nil {
		url := fmt.Sprintf("%s://%s", protocol, host)
		_, hasURLOverride := resourceOverrides[url]
		_, hasHostOverride := resourceOverrides[host]
		if !hasURLOverride && !hasHostOverride {
			realm := extractRealm(wwwauth)
			if realm != "" {
				debugf(1, "Retrying with realm from wwwauth: %s", realm)
				accessToken, expiryUTC, err = getAccessToken(ctx, cred, realm)
			}
		}
	}

	if err == nil && accessToken != "" {
		debugf(1, "Successfully obtained credential")
		outputCredential(accessToken, expiryUTC)
	}
}

func getExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}
	// Resolve any symlinks
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("failed to resolve symlinks: %w", err)
	}
	return exe, nil
}

func runGitConfig(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	debugf(1, "Running: git %s", strings.Join(args, " "))
	return cmd.Run()
}

// checkNetrcForDomains checks if any allowed domains are present in ~/.netrc
// and returns a list of matching domains/hosts found.
func checkNetrcForDomains(domains []string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		debugf(1, "Failed to get home directory: %v", err)
		return nil
	}

	netrcPath := filepath.Join(home, ".netrc")
	file, err := os.Open(netrcPath)
	if err != nil {
		if os.IsNotExist(err) {
			debugf(2, "No .netrc file found at %s", netrcPath)
			return nil
		}
		debugf(1, "Failed to open .netrc: %v", err)
		return nil
	}
	defer file.Close()

	debugf(2, "Checking .netrc at %s", netrcPath)

	var foundHosts []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse "machine <hostname>" entries
		fields := strings.Fields(line)
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "machine" {
				host := strings.ToLower(fields[i+1])
				for _, domain := range domains {
					domain = strings.ToLower(domain)
					if host == domain || strings.HasSuffix(host, "."+domain) {
						debugf(1, "Found matching host in .netrc: %s", host)
						foundHosts = append(foundHosts, host)
					}
				}
			}
		}
	}

	return foundHosts
}

func initCommand(cmd *cobra.Command, args []string) {
	exePath, err := getExecutablePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Check for conflicting .netrc entries
	if netrcHosts := checkNetrcForDomains(defaultAllowedDomains); len(netrcHosts) > 0 {
		fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Found entries in ~/.netrc that may conflict with this credential helper:\n")
		for _, host := range netrcHosts {
			fmt.Fprintf(os.Stderr, "   - %s\n", host)
		}
		fmt.Fprintf(os.Stderr, "\nPlease remove these entries from ~/.netrc to avoid authentication conflicts.\n\n")
	}

	fmt.Println("Configuring git credential helpers...")

	// Set cache helper first (replace any existing)
	if err := runGitConfig("config", "--global", "--replace-all", "credential.helper", "cache"); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting cache helper: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Added cache credential helper")

	// Add this helper
	if err := runGitConfig("config", "--global", "--add", "credential.helper", exePath); err != nil {
		fmt.Fprintf(os.Stderr, "Error adding azure-cli helper: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Added azure-cli credential helper: %s\n", exePath)

	fmt.Println("\nGit credential configuration complete!")
}

func exportsCommand(cmd *cobra.Command, args []string) {
	exePath, err := getExecutablePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	exeDir := filepath.Dir(exePath)

	fmt.Printf("export GOAUTH=\"git %s\"\n", exeDir)
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "git-credential-azure-cli",
		Short: "Git credential helper using Azure CLI credentials",
		Long: `A git credential helper that uses Azure CLI credentials to obtain OAuth tokens
for Azure DevOps, Visual Studio, and other Azure-authenticated git services.

When invoked as a git credential helper (with 'get' argument), it reads
credential request from stdin and outputs bearer token credentials.`,
		// Silently ignore unknown commands per git credential helper spec:
		// "If it does not support the requested operation, it should silently ignore the request."
		Run: func(cmd *cobra.Command, args []string) {
			// Silently exit with success for unknown operations
		},
		// Suppress errors for unknown commands to exit silently
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Add persistent verbose flag
	rootCmd.PersistentFlags().CountVarP(&verbosity, "verbose", "v", "Increase verbosity (use -v, -vv, or -vvv)")

	// Get command (for git credential helper protocol)
	var getCmd = &cobra.Command{
		Use:    "get",
		Short:  "Get credentials (git credential helper protocol)",
		Long:   "Read credential request from stdin and output credentials. This is called by git automatically.",
		Hidden: true, // Hide from help since git calls this
		Run:    getCredential,
	}

	// Init command
	var initCmd = &cobra.Command{
		Use:   "init",
		Short: "Initialize git configuration for credential helper",
		Long: `Configure git to use this credential helper. This will:

1. Set the cache credential helper (to prevent rate limiting)
2. Add this tool as a credential helper

This modifies your global git configuration (~/.gitconfig).`,
		Run: initCommand,
	}

	// Exports command
	var exportsCmd = &cobra.Command{
		Use:   "exports",
		Short: "Output environment variable exports for GOAUTH",
		Long: `Output shell export statements for configuring GOAUTH.

Usage:
  # Add to your shell profile:
  git-credential-azure-cli exports >> ~/.bashrc

  # Or evaluate directly:
  eval $(git-credential-azure-cli exports)`,
		Run: exportsCommand,
	}

	rootCmd.AddCommand(getCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(exportsCmd)

	// Version command
	var versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	}

	rootCmd.AddCommand(versionCmd)

	// Execute the command. Per git credential helper spec, unknown operations
	// should be silently ignored with a successful exit code.
	// Cobra returns an error for unknown subcommands, but we ignore it to
	// comply with the spec: "it should silently ignore the request"
	rootCmd.Execute()
}
