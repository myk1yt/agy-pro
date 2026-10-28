---
name: backup
description: >
  High-speed workspace archiving and backup runbook. Supports lightweight and full backup modes, native robocopy multi-threading, ZIP staging, rotation policies, and automated BACKUP_INFO and RESTORE_GUIDE metadata generation.
---

# Backup Runbook — High-Speed Native Archiving

## 1. Safety Invariants & Pre-Cleanup Assessment
- **Pre-cleanup Check**: Scan the workspace for temporary build files, caches, diagnostics (`zoo-diagnostics-*.json`, `agy-diagnostics-*.json`, `*.log`, `__auto_verify_*`).
- Estimate total temporary size. If significant (>50 MB), prompt the user:
  *"Temporary files (~XX MB) detected. Recommended to run `/cleartemp` before archiving. Proceed with backup now or clean first?"*
- **User-Interaction First**: Confirm target path, backup mode, and output format before executing copy or compression operations.

## 2. Interactive Backup Configuration
Prompt the user with explicit choices:
1. **Backup Mode**:
   - `[A] Lightweight`: Core source code and documentation (`docs/`). Excludes `node_modules`, `.cache`, build output directories, logs, and diagnostic files.
   - `[B] Full`: Entire workspace directory structure including local `.git` history.
2. **Output Format**:
   - `[A] ZIP Archive`: Compressed single ZIP file named `<project>_backup_YYYY-MM-DD.zip`.
   - `[B] Folder Copy`: Direct directory mirroring to the target path.
3. **Target Destination**: User-specified absolute destination path.
4. **Rotation Policy**: If destination already contains >= 5 backups, query:
  *"Target directory contains X previous backups. Delete the oldest backup to save disk space?"*

## 3. Pre-flight Validation
1. Verify target directory existence or create it.
2. Create and immediately delete a temporary probe file (`__write_test_`) at the destination to validate write permissions.

## 4. High-Speed Native Execution
Use native Windows/POSIX CLI utilities for maximum throughput. Never use slow custom scripts for mass file copying.
1. **Folder Copy Execution**:
   - On Windows, execute native `robocopy` with multi-threading (`/MT:32` or `/MT:16`).
   - **CRITICAL Robocopy Exit Code Handling**: Robocopy returns exit codes `0` to `7` for successful operations (e.g., code `1` means files were copied). **Treat exit codes 0 through 7 as SUCCESS**.
   - Lightweight Mode: Pass `/XD node_modules .git .cache target build .dart_tool tmp dist .next` to exclude heavy directories.
     `robocopy <src> <dest> /E /MT:32 /XD node_modules .git .cache target build .dart_tool tmp dist .next`
   - Full Mode: `robocopy <src> <dest> /E /MT:32`
2. **ZIP Archive Execution**:
   - Create a temporary staging directory at `<dest>\__temp_backup_staging`.
   - Copy filtered files into the staging directory using `robocopy`.
   - Write metadata files (`BACKUP_INFO.txt`, `RESTORE_GUIDE.txt`) into the staging directory.
   - Change directory (`cd`) into the staging directory, then run PowerShell `Compress-Archive -Path .\* -DestinationPath <dest_zip_file> -Force` to avoid nesting absolute path trees.
   - Immediately delete the staging directory upon archive completion.

## 5. Metadata Document Generation
Inside the backup directory or ZIP archive root, create:
1. `BACKUP_INFO.txt`: Date, backup mode, format, Git branch name, commit hash (7 chars).
2. `RESTORE_GUIDE.txt`: Step-by-step restoration instructions (cleaning destination, extracting files, reinstalling dependencies).

## 6. Verification & Final Summary
1. Validate archive integrity and file size.
2. Report final backup path, file format, archive size, and metadata inclusion to the user.
