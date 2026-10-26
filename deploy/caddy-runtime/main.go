// The upstream standard Caddy command and modules, with independently patched
// Go/dependency versions. No Caddy application code is forked here.
package main

import (
	caddycmd "github.com/caddyserver/caddy/v2/cmd"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
)

func main() { caddycmd.Main() }
