# Security Policy

## Supported Branches

Security fixes target the `main` branch until release branches are introduced.

## Reporting a Vulnerability

Open a private security advisory on GitHub if the repository is hosted there,
or contact the maintainers through the private coordination channel listed by
the project owner. Do not include secret values, raw tokens, user data, or
private prompts in public issues.

## Scope

The open-source runtime excludes private ScholarLM infrastructure. Reports for
private adapters should identify the adapter boundary and avoid publishing
private endpoint details.

## Secret Handling

WisDev Agent OS must not log or commit API keys, service account JSON, auth
headers, user tokens, or raw private documents. Configuration examples should
document variable names only.
