---
name: pc-health
description: Quick health check of this PC — free disk space, OS, uptime. Use when the user asks how their computer is doing, if they're low on disk space, or about their system.
---

# PC health check

When the user wants to know how their computer is doing:

1. Run the bundled script with bash (it works in Git Bash). Use the skill Directory
   given to you when this skill was loaded:

   ```
   bash "<DIR>/check.sh"
   ```

2. Read the output and explain it in plain, friendly language:
   - How much free disk space there is. If any drive is under ~10% free, gently flag it.
   - The OS, and uptime if shown.

3. Keep it short and reassuring. For non-technical users, avoid jargon entirely — just
   tell them whether things look fine and whether they should free up space.
