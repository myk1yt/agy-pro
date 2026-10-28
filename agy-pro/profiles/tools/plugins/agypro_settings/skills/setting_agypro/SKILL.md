---
name: setting_agypro
description: Discoverability companion for the /setting_agypro plugin slash command - the agy-pro settings session that drives the non-interactive `agy pro settings` verbs (profile, bridge tools, providers, swarm, doctor/keys, install/update). Use when the user wants to manage agy-pro settings from inside the chat.
---

# setting_agypro (skill mirror of the plugin command)

This skill exists so the settings workflow is discoverable from the model side and from `/skills`
listing. The authoritative workflow body is `commands/setting_agypro.md` in this plugin (registered
as the plugin slash command `/setting_agypro`).

## What to do when invoked

Follow `commands/setting_agypro.md` exactly: a numbered menu that drives ONLY the non-interactive
`agy pro settings` verbs via `run_command`, with y/n confirmation before destructive ops.

## Honesty rules (binding)

- This is a **plugin skill/slash command**, the same class as `/agy_help`. It is NOT a core `agy`
  binary command.
- It does **not** contain, embed, or launch the full-screen settings panel. The deterministic
  native-style TUI is `agy pro settings`, launched by the user in their own terminal.
- API key values are never requested or echoed in chat. Entry happens only in the interactive
  wizard (`agy pro setup`), masked as first-5 characters + `****`. Provider add-json accepts an
  environment-variable NAME only.
