package main

import "github.com/creachadair/command"

var helpTopics = []command.HelpTopic{
	{
		Name: "configure",
		Help: `How to configure the plugin.

To run the plugin, install the program somewhere on your system and set the
GOCACHEPROG environment variable to the command line of the plugin. You can
either specify the full path to the program, or install it in your $PATH.

Parameters can be passed either as flags or via environment variables.
See also "help environment".

The plugin requires credentials to access S3. If you are running in AWS, it can
get credentials from the instnce metadata service; otherwise you will need to
plumb AWS environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY) or
set up a configuration file.

See also: "help environment".
Related:  "direct-mode", "serve-mode", "module-proxy", "reverse-proxy".`,
	},
	{
		Name: "environment",
		Help: `Environment variables understood by this program.

To make it easier to configure this tool for multiple workflows, most of the
settings can be set via environment variables as well as flags.

   --------------------------------------------------------------------
   Flag (global)      Variable               Format      Default
   --------------------------------------------------------------------
    --cache-dir       GOCACHE_DIR            path        (required)
    --bucket          GOCACHE_S3_BUCKET      string      (required)
    --region          GOCACHE_S3_REGION      string      based on bucket
    --prefix          GOCACHE_KEY_PREFIX     string      ""
    --min-upload-size GOCACHE_MIN_SIZE       int64       0
    --metrics         GOCACHE_METRICS        bool        false
    --expiry          GOCACHE_EXPIRY         duration    0
    -c                GOCACHE_CONCURRENCY    int         runtime.NumCPU
    -u                GOCACHE_S3_CONCURRENCY duration    runtime.NumCPU
    -v                GOCACHE_VERBOSE        bool        false
    --debug           GOCACHE_DEBUG          bool        false

   --------------------------------------------------------------------
   Flag (serve)       Variable               Format      Default
   --------------------------------------------------------------------
    --plugin          GOCACHE_PLUGIN         port        (required)
    --http            GOCACHE_HTTP           [host]:port ""
    --modproxy        GOCACHE_MODPROXY       bool        false
    --revproxy        GOCACHE_REVPROXY       host,...    ""
    --sumdb           GOCACHE_SUMDB          host,...    ""

See also: "help configure".`,
	},
	{
		Name: "direct-mode",
		Help: `Run the plugin directly as a subprocess of the toolchain.

  export GOCACHEPROG="go-cache-plugin --cache-dir=/tmp/gocache --bucket ..."
  go build ...

Alternatively:

  export GOCACHE_DIR=/tmp/gocache
  export GOCACHE_S3_BUCKET=cache-bucket-name
  export GOCACHEPROG=go-cache-plugin
  go build ...

In this mode, you must specify the --cache-dir and --bucket settings.`,
	},
	{
		Name: "serve-mode",
		Help: `Run the plugin as a standalone service.

Use the "serve" subcommand:

  go-cache-plugin serve \
     --cache-dir=/tmp/gocache --bucket=$B \
     --socket /var/run/cache.sock

You can then use the "connect" subcommand to wire up the toolchain:

  export GOCACHEPROG="go-cache-plugin connect /var/run/cache.sock"

In this mode, the server must have credentials to access to S3, but the
toolchain process does not need AWS credentials.`,
	},
	{
		Name: "module-proxy",
		Help: `Run a Go module and sum database proxy.

With the --modproxy flag, the server will also export an HTTP proxy for the
public Go module proxy (proxy.golang.org) and sum DB (sum.golang.org) at the
given address:

   go-cache-plugin serve ... --http=localhost:5970 --modproxy

To use the module proxy, set the standard GOPROXY environment variable:

   export GOPROXY=localhost:5970
   export GOCACHEPROG="go-cache-plugin connect /var/run/cache.sock"
   go build ...

To use the sum DB proxy, set the GOSUMDB environment variable:

   export GOSUMDB="sum.golang.org http://localhost:5970/sumdb/sum.golang.org"

See also: https://proxy.golang.org/`,
	},
	{
		Name: "reverse-proxy",
		Help: `Run a caching reverse proxy.

With the --revproxy flag, the server will also export a caching reverse
proxy for the specified hosts, given as a comma-separated list:

   go-cache-plugin serve ... \
      --http=localhost:5970 \
      --revproxy='api.example.com,*.example2.com'

When this is enabled, you can configure this address as an HTTP proxy:

   HTTP_PROXY=localhost:5970 curl https://api.example.com/foo`,
	},
}
