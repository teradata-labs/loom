#!/bin/bash
set -e

echo "=== Loom Test Data Cleanup ==="
echo
echo "This script will:"
echo "  1. Backup all databases"
echo "  2. Clear test sessions and messages"
echo "  3. Clear observability traces"
echo "  4. Keep test scripts for regression testing"
echo

read -p "Continue? (y/n): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Cancelled."
    exit 0
fi

echo

# Step 1: Backup databases
echo "Step 1: Backing up databases..."
BACKUP_DIR=~/.loom/backups/$(date +%Y%m%d_%H%M%S)
mkdir -p "$BACKUP_DIR"

cp ~/.loom/loom.db "$BACKUP_DIR/loom.db.backup"
cp ~/.loom/observability.db "$BACKUP_DIR/observability.db.backup"
cp ~/.loom/hitl.db "$BACKUP_DIR/hitl.db.backup" 2>/dev/null || true
cp ~/.loom/scheduler.db "$BACKUP_DIR/scheduler.db.backup" 2>/dev/null || true

echo "✅ Databases backed up to: $BACKUP_DIR"
echo

# Step 2: Show what will be deleted
echo "Step 2: Current database state..."
SESSION_COUNT=$(sqlite3 ~/.loom/loom.db "SELECT COUNT(*) FROM sessions;")
MESSAGE_COUNT=$(sqlite3 ~/.loom/loom.db "SELECT COUNT(*) FROM messages;")
OBS_SIZE=$(du -h ~/.loom/observability.db | cut -f1)

echo "Sessions: $SESSION_COUNT"
echo "Messages: $MESSAGE_COUNT"
echo "Observability DB size: $OBS_SIZE"
echo

# Step 3: Clear sessions and messages
echo "Step 3: Clearing test sessions and messages..."
# FTS5 virtual tables block DELETE operations, so we'll remove and recreate the DB
echo "  Removing loom.db (will be recreated on server start)..."
rm -f ~/.loom/loom.db
echo "  ✅ Database cleared (server will recreate schema on restart)"

echo "✅ Cleared sessions, messages, and tool executions"
echo

# Step 4: Clear observability traces
echo "Step 4: Clearing observability traces..."
sqlite3 ~/.loom/observability.db "DELETE FROM spans; DELETE FROM traces; VACUUM;" 2>/dev/null || true
echo "✅ Cleared observability traces"
echo

# Step 5: Verify cleanup
echo "Step 5: Verifying cleanup..."
if [ -f ~/.loom/loom.db ]; then
    NEW_SESSION_COUNT=$(sqlite3 ~/.loom/loom.db "SELECT COUNT(*) FROM sessions;" 2>/dev/null || echo "0")
    NEW_MESSAGE_COUNT=$(sqlite3 ~/.loom/loom.db "SELECT COUNT(*) FROM messages;" 2>/dev/null || echo "0")
else
    NEW_SESSION_COUNT="deleted"
    NEW_MESSAGE_COUNT="deleted"
fi
NEW_OBS_SIZE=$(du -h ~/.loom/observability.db 2>/dev/null | cut -f1 || echo "0")

echo "Sessions: $SESSION_COUNT → $NEW_SESSION_COUNT"
echo "Messages: $MESSAGE_COUNT → $NEW_MESSAGE_COUNT"
echo "Observability DB size: $OBS_SIZE → $NEW_OBS_SIZE"
echo

# Step 6: Summary
echo "=== Cleanup Complete ==="
echo
echo "✅ Kept:"
echo "  - Test scripts (test_*.sh) for regression testing"
echo "  - Agent configurations"
echo "  - Pattern libraries"
echo "  - hitl.db and scheduler.db"
echo
echo "✅ Cleared:"
echo "  - $SESSION_COUNT test sessions"
echo "  - $MESSAGE_COUNT test messages"
echo "  - Observability traces"
echo
echo "✅ Backup location:"
echo "  $BACKUP_DIR"
echo
echo "To restore from backup:"
echo "  cp $BACKUP_DIR/loom.db.backup ~/.loom/loom.db"
echo "  cp $BACKUP_DIR/observability.db.backup ~/.loom/observability.db"
