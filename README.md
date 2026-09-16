# dn-mcp

An MCP server for managing a Defined Networking (Nebula) network from Claude Code
or any other MCP-capable agent.

It exposes the Defined Networking admin API as MCP tools over stdio: list and
inspect networks, hosts, roles, tags and routes; run the full host lifecycle —
create, enroll, update, block, delete; and manage firewall policy on roles and
tags.

## Safety model

**Hosts, roles and tags are writable. Networks and routes are read-only.**

Role and tag writes are the part of this tool set that escalates. Both carry
inbound firewall rules, so `update_role` or `update_tag` can open a port to
every host holding that role or tag. A host's firewall is the union of its
role's rules and the rules of every tag it carries, so a rule added to a tag
applies across roles. Network mutation stays excluded — `networks:delete` is
unrecoverable — and so does route mutation.

The tools are only half of the boundary. **Scope the API key to what the agent
actually needs**, so the limit is enforced by the server rather than by the
agent's good behaviour. An agent acting on a misread instruction cannot open a
firewall port if the key it holds has no `roles:update` or `tags:update`
permission. For day-to-day host work, leave the policy permissions off the key.

Grant the key only what you need:

| Capability                          | Permissions |
| ----------------------------------- | ----------- |
| Read-only (recommended to start)    | `hosts:list`, `hosts:read`, `networks:list`, `networks:read`, `roles:list`, `roles:read`, `routes:list`, `routes:read`, `tags:list`, `tags:read` |
| Host management | the above, plus `hosts:create`, `hosts:update`, `hosts:delete`, `hosts:enroll`, `hosts:block`, `hosts:unblock` |
| Live diagnostics | the above, plus `hosts:debug` |
| Firewall policy (the full tool set) | the above, plus `roles:create`, `roles:update`, `roles:delete`, `tags:create`, `tags:update`, `tags:delete` |

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

`PUT /v3/hosts/{hostID}`, `PUT /v1/roles/{roleID}` and `PUT /v1/tags/{tag}`
are all **full replacements, not patches**. Hosts come first below; roles and
tags follow.

For a host, every field in the body overwrites stored state, and an omitted
field is written as its zero value.

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

Roles and tags have the same trap, with a firewall-sized blast radius:

- **Roles.** Omitting `firewallRules` from the PUT deletes every rule on the
  role. `update_role` reads the role and resends its rules unless the caller
  replaces them. If the read comes back without a rules field at all, the
  update is refused rather than sent. Roles cannot be renamed through the API.
- **Tags.** The tag PUT is inconsistent: an omitted `firewallRules` leaves the
  rules alone, but an omitted `configOverrides`, `description` or
  `routeSubscriptions` is erased. `update_tag` resends those three every time
  and only sends `firewallRules` when the caller changes them. It never sends
  `before`/`after`, so a tag's priority does not move.
- `firewallRules` is a **whole-list replacement** on both. To add one rule,
  pass the existing rules plus the new one — `get_role` or `get_tag` first. Pass
  an empty list to remove all rules.

## Tools

### Read-only

| Tool | Description |
| ---- | ----------- |
| `list_hosts` | List hosts, filtered by search, network, role, tags, blocked/lighthouse/relay status, platform, never-seen, or update-available |
| `get_host` | One host, including tags, role, addresses and last-seen metadata |
| `list_networks` / `get_network` | Networks and their CIDR ranges |
| `list_roles` / `get_role` | Roles and their full firewall rule sets |
| `list_routes` / `get_route` | Unsafe routes |
| `list_tags` | Tags defined in the organization. Does not include firewall rules |
| `get_tag` | One tag, including its firewall rules, config overrides and route subscriptions |

### Host lifecycle

| Tool | Description |
| ---- | ----------- |
| `create_host` | Create a host in a network |
| `create_host_and_enrollment_code` | Create a host and issue an enrollment code in one step — the usual way to onboard a device |
| `update_host` | Update name, role, listen port, static addresses or tags (read-modify-write) |
| `create_enrollment_code` | Issue a fresh enrollment code for an existing host |
| `block_host` / `unblock_host` | Revoke and restore network access |
| `delete_host` | Permanently delete a host — irreversible, prefer `block_host` |

### Firewall policy

| Tool | Description |
| ---- | ----------- |
| `create_role` | Create a role with inbound firewall rules |
| `update_role` | Change a role's description or firewall rules (read-modify-write) |
| `delete_role` | Permanently delete a role — irreversible |
| `create_tag` | Create a tag, optionally with inbound firewall rules |
| `update_tag` | Change a tag's description, firewall rules or route subscriptions (read-modify-write) |
| `delete_tag` | Permanently delete a tag, removing it and its rules from every host — irreversible |

Each rule takes a `protocol` (`ANY`, `TCP`, `UDP`, `ICMP`), an optional
`portRange` (omit for all ports), and an optional source: `allowedRoleID`,
`allowedTags`, or both, which are ANDed. A rule with no source allows every
host in the network.

### Live diagnostics

These reach the dnclient running on the machine, so the host must be **online**.
An offline host returns a not-reachable error rather than stale data.

| Tool | Description |
| ---- | ----------- |
| `run_host_diagnostic` | Run one read-only command: `Ping`, `ListCommands`, `PrintCert`, `PrintTunnel`, `QueryLighthouse`, `CreateTunnel`, `DebugStack` |
| `stream_host_logs` | Collect live logs for a fixed window (1-600s, default 15) at a given level |
| `restart_host_client` | Restart the dnclient — briefly drops tunnels, changes no config |

`PrintCert`, `PrintTunnel`, `QueryLighthouse` and `CreateTunnel` take a `target`
Nebula IP, from another host's `ipAddresses`. A `null` result from these usually
means the target is offline or has never been seen by this host, which is itself
the answer to "why can't these two talk?".

`Restart` is kept out of `run_host_diagnostic`'s command list on purpose. It is
disruptive, so it gets its own tool and its own approval prompt rather than
riding in as one enum value among several harmless ones.

**These block.** A diagnostic waits up to ~45s; `stream_host_logs` blocks for its
whole window. Logs are only produced while the window is open, so trigger the
behaviour you are debugging during it.

List results are paginated. Responses carry a `pagination` object; pass its
`nextCursor` back as `cursor` to continue.

## Development

```bash
make ready   # fmt, vet, test, build
```

The tests cover the read-modify-write merges for hosts, roles and tags, which
are the parts most likely to cause real damage if they regress — for roles and
tags, a regression silently deletes firewall rules.

## Not yet covered

- **Triggering client updates.** dnclient reports supporting `DoUpdate` and
  `DoConfigUpdate`, but the API's command allow-list does not relay them, so
  there is no way to push an update from here. Updates happen on the machine.
- Audit logs. `audit-logs:list` is organization-wide and excluded from scoped
  keys, so it sits outside this server's boundary.
- Network and route mutation — excluded by design, see above.
- Tag ordering. `create_tag` and `update_tag` do not expose `before`/`after`,
  so tag priority, which decides which tag's config overrides win, is managed
  in the admin UI.
