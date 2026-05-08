package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"wlchat/internal/gemini"
	"wlchat/internal/geminicli"
	"wlchat/internal/groq"
	"wlchat/internal/provider"
	"wlchat/internal/store"
	"wlchat/internal/ui"
)

const appName = "wlchat"

func envKey(name string) string { return strings.TrimSpace(os.Getenv(name)) }

func dataDir() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(xdg, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", appName), nil
}

func configDir() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", appName), nil
}

func main() {
	var providers []provider.Provider
	if k := envKey("GROQ_API_KEY"); k != "" {
		fmt.Printf("info: detected GROQ_API_KEY, initializing groq provider\n")
		providers = append(providers, groq.NewClient(k))
	}
	if k := envKey("GEMINI_API_KEY"); k != "" {
		fmt.Printf("info: detected GEMINI_API_KEY, initializing gemini provider\n")
		providers = append(providers, gemini.NewClient(k))
	}
	if geminicli.Available() {
		fmt.Printf("info: gemini CLI binary found, initializing gemini-cli provider\n")
		providers = append(providers, geminicli.NewClient())
	} else {
		fmt.Printf("info: gemini CLI binary not found in PATH\n")
	}

	if len(providers) == 0 {
		fmt.Fprintln(os.Stderr, "wlchat: no providers — set GROQ_API_KEY or GEMINI_API_KEY, or install the gemini CLI")
		os.Exit(1)
	}

	fmt.Printf("info: initialized %d providers: ", len(providers))
	var names []string
	for _, p := range providers {
		names = append(names, p.Name())
	}
	fmt.Printf("%s\n", strings.Join(names, ", "))

	dDir, err := dataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wlchat data dir:", err)
		os.Exit(1)
	}
	cDir, err := configDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wlchat config dir:", err)
		os.Exit(1)
	}
	s, err := store.New(dDir, cDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wlchat store:", err)
		os.Exit(1)
	}

	if err := ui.Run(s, providers); err != nil {
		fmt.Fprintln(os.Stderr, "wlchat:", err)
		os.Exit(1)
	}
}
