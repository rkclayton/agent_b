# Import from Hermes

Open Settings → Profiles and choose **Import from Hermes**. Point it at a Hermes home such as `~/.hermes` or one of its profile folders.

Agent_b previews the import before writing anything. Choose the memory files and skills to include, then confirm. Included skills are copied into the active profile, and Hermes paths are rewritten so the source folder is no longer required. A skill with an unmapped Hermes-only tool call arrives switched off and the report names each call.

The preview deliberately excludes the persona, scheduled jobs, sessions, model configuration, authentication file, and secret values. Secret names are shown so a later credential import can identify them, but this release does not read or store their values.

Importing the same folder again changes nothing. If you edit an imported skill, a later import preserves your edit.
