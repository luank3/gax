package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/slash"
	terminalpaste "github.com/luank3/gax/internal/terminal/paste"
	terminalpicker "github.com/luank3/gax/internal/terminal/picker"
	"github.com/luank3/gax/internal/transport"
)

func runLoginCommand(ctx context.Context, parsed slash.Parsed, stdin io.Reader, liveTTY bool, store *config.Store,
	resolver *runtime.Resolver, currentProvider **runtime.Provider, stdout, stderr io.Writer) {
	home, stateHome := authDirectories(store)
	requested := strings.TrimSpace(parsed.Argument)
	if !parsed.HasArgument || requested == "" {
		if !liveTTY {
			writeNoteLine(stdout, "/login needs a provider name when not interactive")
			return
		}
		if !chooseCodexMethod("log in to", "ChatGPT account (browser or device login)", codex.LoginStatus(home, stateHome), stdin, stdout, stderr) {
			return
		}
		requested = "codex"
	}
	if registry.CanonicalID(requested) != "codex" {
		fmt.Fprintf(stdout, "no login flow for '%s' — gax manages logins for: codex\n", terminalSafeText(requested))
		return
	}

	flow := "device"
	if liveTTY {
		index, opened, err := terminalpicker.RunTTY(terminalpicker.Options{
			Title: "log in to ChatGPT with",
			Items: []terminalpicker.Item{
				{Label: "browser", Detail: "sign in at auth.openai.com on this machine"},
				{Label: "device code", Detail: "enter a code shown here — works over ssh"},
			},
		}, stdinFile(stdin), stdout)
		if err != nil {
			fmt.Fprintf(stderr, "gax: login picker: %v\n", err)
			return
		}
		if !opened || index < 0 || index > 1 {
			return
		}
		if index == 0 {
			flow = "browser"
		}
	}

	options := codex.LoginOptions{Context: ctx, Home: home, StateHome: stateHome, HTTPClient: transport.DefaultClient}
	options.Busy = func(label string) (func() bool, func() bool) {
		busy := startBusyOperation(ctx, stdin, liveTTY, stdout, label)
		return busy.Tick, func() bool { return endBusyOperation(busy, stderr) }
	}
	options.ShowDeviceCode = func(userCode, verifyURL string) {
		copied := terminalpaste.CopyClipboard(ctx, userCode, stdout) == nil
		fmt.Fprintf(stdout, "  open %s\n  and enter code %s", terminalSafeText(verifyURL), terminalSafeText(userCode))
		if copied {
			fmt.Fprint(stdout, " (copied to clipboard)")
		}
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout)
	}
	options.ShowBrowserLink = func(loginURL string) {
		copied := terminalpaste.CopyClipboard(ctx, loginURL, stdout) == nil
		fmt.Fprintln(stdout, "  approve the login in your browser, or open")
		fmt.Fprintln(stdout, terminalSafeText(loginURL))
		if copied {
			fmt.Fprintln(stdout, "  (link copied to clipboard)")
		}
		fmt.Fprintln(stdout)
	}
	var result codex.LoginResult
	var err error
	if flow == "browser" {
		result, err = codex.LoginBrowser(options)
	} else {
		result, err = codex.LoginDevice(options)
	}
	if err != nil {
		if !result.Cancelled {
			fmt.Fprintf(stderr, "gax: %v\n", err)
		}
		return
	}
	if result.Email != "" {
		writeNoteLine(stdout, fmt.Sprintf("logged in as %s — gax now manages this token", terminalSafeText(result.Email)))
	} else {
		writeNoteLine(stdout, "logged in — gax now manages this token")
	}
	if *currentProvider != nil && (*currentProvider).Definition != nil && (*currentProvider).Definition.ID == "codex" {
		if refreshed, resolveErr := resolver.Resolve("codex"); resolveErr == nil {
			replaceProvider(currentProvider, refreshed)
		} else {
			writeWarningDiagnostic(stderr, "could not reload Codex login: %v", resolveErr)
		}
	} else {
		writeNoteLine(stdout, "run /provider to switch to Codex")
	}
}

func runLogoutCommand(ctx context.Context, parsed slash.Parsed, stdin io.Reader, liveTTY bool, store *config.Store,
	resolver *runtime.Resolver, currentProvider **runtime.Provider, stdout, stderr io.Writer) {
	home, stateHome := authDirectories(store)
	requested := strings.TrimSpace(parsed.Argument)
	if !parsed.HasArgument || requested == "" {
		managed, err := codex.HasManagedAuth(home, stateHome)
		if err != nil {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return
		}
		if !managed {
			writeNoteLine(stdout, "no gax-managed logins to remove")
			return
		}
		if !liveTTY {
			writeNoteLine(stdout, "/logout needs a provider name when not interactive")
			return
		}
		if !chooseCodexMethod("log out of", "Codex", codex.LoginStatus(home, stateHome), stdin, stdout, stderr) {
			return
		}
		requested = "codex"
	}
	if registry.CanonicalID(requested) != "codex" {
		fmt.Fprintf(stdout, "no login flow for '%s' — gax manages logins for: codex\n", terminalSafeText(requested))
		return
	}
	removed, warning, err := codex.LogoutManaged(ctx, home, stateHome, transport.DefaultClient, "")
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return
	}
	if !removed {
		writeNoteLine(stdout, "no gax-managed login for Codex")
		return
	}
	writeNoteLine(stdout, "logged out of Codex")
	if warning != "" {
		writeNoteLine(stdout, "warning: "+warning)
	}
	if *currentProvider != nil && (*currentProvider).Definition != nil && (*currentProvider).Definition.ID == "codex" {
		if refreshed, resolveErr := resolver.Resolve("codex"); resolveErr == nil {
			replaceProvider(currentProvider, refreshed)
		} else {
			writeWarningDiagnostic(stderr, "could not reload Codex credentials: %v", resolveErr)
		}
	}
}

func chooseCodexMethod(title, detail, status string, stdin io.Reader, stdout, stderr io.Writer) bool {
	index, opened, err := terminalpicker.RunTTY(terminalpicker.Options{
		Title: title,
		Items: []terminalpicker.Item{{Label: "Codex", Detail: detail, Description: status}},
	}, stdinFile(stdin), stdout)
	if err != nil {
		fmt.Fprintf(stderr, "gax: login picker: %v\n", err)
		return false
	}
	return opened && index == 0
}

func authDirectories(store *config.Store) (home, stateHome string) {
	home, _ = os.UserHomeDir()
	if value, exists := store.LookupEnv("XDG_STATE_HOME"); exists {
		stateHome = value
	}
	return home, stateHome
}
