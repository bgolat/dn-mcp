# dn-mcp

An MCP server for managing a Defined Networking (Nebula) network from Claude Code
or any other MCP-capable agent.

It exposes the Defined Networking admin API as MCP tools over stdio: list and
inspect networks, hosts, roles and routes, and run the full host lifecycle —
create, enroll, update, block, delete.

## Safety model

The tool set mirrors `data.ScopedKeyAllowedPermissions` in the API: **the host
lifecycle is writable, everything else is read-only.**

Role, route, network and tag mutation are deliberately excluded. That is the
same boundary the API draws for role-scoped keys, and for the same reason —
`roles:update` edits firewall rules, so it escalates, and `networks:delete` is
unrecoverable.

Leaving those tools out is only half of it. **Scope the API key the same way**,
so the boundary is enforced by the server rather than by the agent's good
behaviour. An agent acting on a misread instruction cannot delete a network if
the key it holds has no `networks:delete` permission.

Grant the key only what you need:

| Capability                          | Permissions |
| ----------------------------------- | ----------- |
| Read-only (recommended to start)    | `hosts:list`, `hosts:read`, `networks:list`, `networks:read`, `roles:list`, `roles:read`, `routes:list`, `routes:read`, `tags:list`, `tags:read` |
| Host management (the full tool set) | the above, plus `hosts:create`, `hosts:update`, `hosts:delete`, `hosts:enroll`, `hosts:block`, `hosts:unblock` |

Tools whose permissions the key lacks still appear in the list, but fail with
the API's own permission error when called.

## Install

```bash
make install
```

That puts `dn-mcp` in `$(go env GOPATH)/bin`. Use `make build` instead for a
binary in the working directory.

## Configure

| Variable     | Required | Description |
| ------------ | -------- | ----------- |
| `DN_API_KEY` | yes      | A Defined Networking API key |
| `DN_API_URL` | no       | API base URL, defaults to `https://api.defined.net` |

## Wire it into Claude Code

```bash
claude mcp add defined-networking --env DN_API_KEY=dnkey-your-key-here -- dn-mcp
```

Then ask it things:

- "Which hosts haven't connected in a while?"
- "Create a host called `laptop-03` in my main network and give me an enrollment code."
- "Why can't `web-01` reach the database host?"
- "Block `contractor-laptop`."

## Design note: updates are read-modify-write

`PUT /v3/hosts/{hostID}` is a **full replacement, not a patch**. Every field in
the body overwrites stored state, and an omitted field is written as its zero
value.

That is a poor match for how an agent phrases intent. "Change the listen port
to 4243" naturally becomes a request carrying only `listenPort` — which, sent
straight through, would also clear the host's name, role, tags, static
addresses and config overrides.

So `update_host` reads the host first, merges the fields the caller actually
set, and sends the complete body. Callers get patch semantics; the API gets the
full replacement it expects. If the read fails, the write is abandoned rather
than risk PUTting a zero-valued host.

Two consequences worth knowing:

- `tags` and `staticAddresses` are **whole-list replacements**. To add one tag,
  pass the existing tags plus the new one. `get_host` first.
- **Config overrides are preserved but not editable** through this server. They
  ride through the read-modify-write untouched.

## Tools

### Read-only

| Tool | Description |
| ---- | ----------- |
| `list_hosts` | List hosts, filtered by search, network, role, tags, blocked/lighthouse/relay status, platform, never-seen, or update-available |
| `get_host` | One host, including tags, role, addresses and last-seen metadata |
| `list_networks` / `get_network` | Networks and their CIDR ranges |
| `list_roles` / `get_role` | Roles and their full firewall rule sets |
| `list_routes` / `get_route` | Unsafe routes |
| `list_tags` | Tags defined in the organization |

### Host lifecycle

| Tool | Description |
| ---- | ----------- |
| `create_host` | Create a host in a network |
| `create_host_and_enrollment_code` | Create a host and issue an enrollment code in one step — the usual way to onboard a device |
| `update_host` | Update name, role, listen port, static addresses or tags (read-modify-write) |
| `create_enrollment_code` | Issue a fresh enrollment code for an existing host |
| `block_host` / `unblock_host` | Revoke and restore network access |
| `delete_host` | Permanently delete a host — irreversible, prefer `block_host` |

List results are paginated. Responses carry a `pagination` object; pass its
`nextCursor` back as `cursor` to continue.

## Development

```bash
make ready   # fmt, vet, test, build
```

The tests cover the read-modify-write merge, which is the part most likely to
cause real damage if it regresses.

## Not yet covered

- `POST /v1/hosts/{hostID}/command` (`hosts:debug`) — the dnclient debug
  commands, including `StreamLogs` and `QueryLighthouse`. It is a streaming
  endpoint and needs different handling from the JSON tools here.
- Audit logs. `audit-logs:list` is organization-wide and excluded from scoped
  keys, so it sits outside this server's boundary.
- Network, role, route and tag mutation — excluded by design, see above.
