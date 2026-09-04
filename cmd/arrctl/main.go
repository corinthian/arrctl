// Command arrctl is a JSON-first CLI over the Radarr and Sonarr v3 HTTP APIs.
//
//	arrctl <app> <group> <verb> [flags]
//
// <app> is radarr or sonarr, always the first positional and always required.
package main

import "github.com/corinthian/arrctl/internal/commands"

func main() { commands.Execute() }
