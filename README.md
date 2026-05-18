# bitcreds

Credential leak scanner for on-premise Bitbucket Server. Searches code via the Bitbucket code search API (Elasticsearch), stores findings in SQLite, and provides a web UI for triaging results.

## Requirements

- Go 1.21+
- Bitbucket Server with the code search plugin (Elasticsearch) enabled
- A personal access token with repo read access

## Build

```bash
make
```

Produces `bin/bitcreds-scan` and `bin/bitcreds-server`.

## Configuration

Copy the example env file and fill in your values:

```bash
cp .env.example .env
```

```
BITBUCKET_URL=https://bitbucket.internal.com
BITBUCKET_TOKEN=your-token
BITCREDS_USERNAME=admin
BITCREDS_PASSWORD=yourpassword
```

Both binaries read `.env` from the working directory. CLI flags and real environment variables take precedence.

## Usage

### Scanner

```bash
# Basic scan (code search API, current state)
./bin/bitcreds-scan --timeout 1h

# With custom rate limit and concurrency
./bin/bitcreds-scan --rate-limit 5 --concurrency 3 --timeout 30m

# History mode (clones repos, scans git diffs)
./bin/bitcreds-scan --history --history-depth 60d --timeout 2h

# All flags
./bin/bitcreds-scan --help
```

### Web Server

```bash
./bin/bitcreds-server --listen :8080
```

Then open `http://localhost:8080` and log in with your configured credentials.

### Custom Patterns

Add patterns to a YAML file and pass it with `--patterns`:

```yaml
patterns:
  - name: internal_api_key
    type: api_token
    severity: high
    regex: 'INTERNAL-[A-Z0-9]{32}'
    description: Internal service API key
```

```bash
./bin/bitcreds-scan --patterns custom.yaml
```

Custom patterns are merged with the 14 built-in defaults (AWS keys, private keys, GitHub tokens, Slack tokens, database URLs, JWTs, etc.).

## Project Structure

```
cmd/bitcreds-scan/       Scanner CLI
cmd/bitcreds-server/     Web server
internal/bitbucket/      API client with rate limiting
internal/scanner/        Scan orchestration and history scanning
internal/patterns/       Pattern loading and matching
internal/db/             SQLite data access layer
internal/web/            HTTP handlers, templates, static assets
internal/config/         .env file loader
patterns.yaml            Default pattern definitions
```

## Notes

- TLS verification is disabled by default to handle self-signed certs common on internal servers.
- The scanner deduplicates findings by file + line + pattern so re-running won't create duplicates for existing open findings.
- The web UI uses basic auth. Run it behind a reverse proxy if you need TLS for the UI itself.
