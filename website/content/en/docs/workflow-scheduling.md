---
title: "Workflow Scheduling Guide"
linkTitle: "Workflow Scheduling"
weight: 40
description: >
  Comprehensive guide to cron-based workflow scheduling in Loom
---

## Overview

Loom's workflow scheduler enables automatic execution of workflows based on cron expressions. The scheduler provides:

- **Persistence**: SQLite-backed schedules survive server restarts
- **Hot-reload**: YAML files automatically reload when changed
- **Concurrency Control**: Prevent overlapping executions with `skip_if_running`
- **Timezone Support**: Schedule workflows in any IANA timezone
- **Execution History**: Track success, failure, and timing metrics
- **Dual Configuration**: YAML files for static schedules + gRPC API for dynamic scheduling

## Quick Start

### 1. Create a Scheduled Workflow

Create `~/.loom/workflows/daily-report.yaml`:

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: daily-sales-report
  description: Generate and distribute daily sales report

spec:
  type: pipeline
  pipeline:
    initial_prompt: "Generate comprehensive daily sales report for yesterday"
    stages:
      - agent_id: data-analyst
        prompt: |
          Query the sales database and generate a summary report.

      - agent_id: report-formatter
        prompt: |
          Format the sales data into a professional HTML report.

# Schedule Configuration
schedule:
  cron: "0 8 * * *"              # Every day at 8:00 AM
  timezone: "America/New_York"   # Eastern Time
  enabled: true
  skip_if_running: true
  max_execution_seconds: 1800    # 30 minutes
  variables:
    report_type: "daily"
    environment: "production"
```

### 2. Enable the Scheduler

Add to `~/.loom/looms.yaml`:

```yaml
scheduler:
  enabled: true
  workflow_dir: "~/.loom/workflows"
  hot_reload: true
```

### 3. Start the Server

```bash
looms serve
```

The scheduler will:
1. Load all workflow YAML files from `workflow_dir`
2. Parse cron expressions and calculate next execution times
3. Execute workflows automatically at scheduled times
4. Watch for YAML file changes and hot-reload

## Schedule Configuration Reference

```yaml
schedule:
  # Cron expression (5-field format: minute hour day month weekday)
  cron: "0 8 * * *"

  # Timezone for cron evaluation (default: UTC)
  # Use IANA timezone names: America/New_York, Europe/London, Asia/Tokyo
  timezone: "UTC"

  # Enable/disable the schedule (default: true)
  enabled: true

  # Skip execution if previous run is still in progress (default: true)
  # Recommended: true for data pipelines to prevent conflicts
  skip_if_running: true

  # Maximum execution time in seconds (default: 3600 = 1 hour)
  max_execution_seconds: 1800

  # Variables passed to the workflow (optional)
  variables:
    key1: "value1"
    key2: "value2"
```

## Cron Expression Format

Standard 5-field cron format:

```
┌───────────── minute (0 - 59)
│ ┌───────────── hour (0 - 23)
│ │ ┌───────────── day of month (1 - 31)
│ │ │ ┌───────────── month (1 - 12)
│ │ │ │ ┌───────────── day of week (0 - 6) (Sunday = 0)
│ │ │ │ │
* * * * *
```

### Common Patterns

| Schedule | Cron Expression | Description |
|----------|----------------|-------------|
| Every minute | `* * * * *` | Runs every minute |
| Every 5 minutes | `*/5 * * * *` | Runs every 5 minutes |
| Every hour | `0 * * * *` | Runs at minute 0 of every hour |
| Every 6 hours | `0 */6 * * *` | Runs at 00:00, 06:00, 12:00, 18:00 |
| Daily at midnight | `0 0 * * *` | Runs at 00:00 every day |
| Daily at 8 AM | `0 8 * * *` | Runs at 08:00 every day |
| Every Monday at 9 AM | `0 9 * * 1` | Runs at 09:00 every Monday |
| First day of month | `0 0 1 * *` | Runs at 00:00 on the 1st of each month |
| Every Sunday | `0 0 * * 0` | Runs at 00:00 every Sunday |
| Weekdays at 6 PM | `0 18 * * 1-5` | Runs at 18:00 Monday through Friday |

### Advanced Patterns

```yaml
# Every 15 minutes during business hours (9 AM - 5 PM, Monday-Friday)
cron: "*/15 9-17 * * 1-5"

# First Monday of every month at 10 AM
# Note: Cron doesn't directly support "first Monday", use day 1-7 with Monday
cron: "0 10 1-7 * 1"

# Every 2 hours during the day (8 AM - 8 PM)
cron: "0 8-20/2 * * *"

# Multiple times per day (8 AM, 12 PM, 6 PM)
# Note: Requires multiple schedules or use ranges
cron: "0 8,12,18 * * *"
```

## Usage Patterns

### YAML-Based Schedules (Recommended)

**Best for**: Static schedules, version-controlled workflows, team collaboration

**Advantages**:
- Version control via git
- Hot-reload on file changes
- Code review process
- Documentation alongside schedule

**Example**:
```bash
# Create workflow
mkdir -p ~/.loom/workflows
cat > ~/.loom/workflows/hourly-sync.yaml << 'EOF'
apiVersion: loom/v1
kind: Workflow
metadata:
  name: hourly-data-sync

spec:
  type: pipeline
  pipeline:
    initial_prompt: "Synchronize customer data from CRM to warehouse"
    stages:
      - agent_id: data-extractor
      - agent_id: data-transformer
      - agent_id: data-loader

schedule:
  cron: "0 * * * *"
  timezone: "UTC"
  enabled: true
  skip_if_running: true
EOF

# Start server (auto-loads workflows)
looms serve
```

### gRPC API-Based Schedules

**Best for**: Dynamic schedules, runtime management, API-driven workflows

**Advantages**:
- Programmatic creation
- Runtime modifications
- No file system access needed
- Ephemeral schedules

**Example**:
```bash
# Create schedule via gRPC
grpcurl -d '{
  "workflow_name": "on-demand-backup",
  "pattern": {
    "pipeline": {
      "initial_prompt": "Run incremental backup",
      "stages": [{"agent_id": "backup-worker"}]
    }
  },
  "schedule": {
    "cron": "0 */6 * * *",
    "timezone": "UTC",
    "enabled": true,
    "skip_if_running": true
  }
}' localhost:50051 loom.v1.LoomService/ScheduleWorkflow

# Returns: {"schedule_id": "rpc-on-demand-backup-1234567890-123456789", ...}
```

### Schedule Management Comparison

| Feature | YAML-Sourced | gRPC-Created |
|---------|-------------|--------------|
| Source | `~/.loom/workflows/*.yaml` | gRPC API calls |
| Hot-reload | ✅ Yes | ❌ No |
| Modifiable via RPC | ❌ No (edit YAML) | ✅ Yes |
| Deletable via RPC | ❌ No (delete YAML) | ✅ Yes |
| Persistence | File + SQLite | SQLite only |
| Best for | Static schedules, version control | Dynamic schedules, runtime management |
| Version control | ✅ Yes | ❌ No |

## gRPC API Reference

### ScheduleWorkflow

Create a new scheduled workflow via RPC.

```bash
grpcurl -d '{
  "workflow_name": "my-workflow",
  "pattern": {
    "pipeline": {
      "initial_prompt": "Run task",
      "stages": [{"agent_id": "worker"}]
    }
  },
  "schedule": {
    "cron": "0 */6 * * *",
    "timezone": "UTC",
    "enabled": true,
    "skip_if_running": true,
    "max_execution_seconds": 3600,
    "variables": {"env": "prod"}
  }
}' localhost:50051 loom.v1.LoomService/ScheduleWorkflow
```

**Response**:
```json
{
  "schedule_id": "rpc-my-workflow-1735516800-123456789",
  "schedule": {
    "id": "rpc-my-workflow-1735516800-123456789",
    "workflow_name": "my-workflow",
    "next_execution_at": "1735520400"
  }
}
```

### UpdateScheduledWorkflow

Update an existing RPC-created schedule. **Note**: YAML-sourced schedules cannot be updated via RPC.

```bash
grpcurl -d '{
  "schedule_id": "rpc-my-workflow-1735516800-123456789",
  "schedule": {
    "cron": "0 8 * * *",
    "enabled": false
  }
}' localhost:50051 loom.v1.LoomService/UpdateScheduledWorkflow
```

### GetScheduledWorkflow

Retrieve schedule details.

```bash
grpcurl -d '{
  "schedule_id": "rpc-my-workflow-1735516800-123456789"
}' localhost:50051 loom.v1.LoomService/GetScheduledWorkflow
```

### ListScheduledWorkflows

List all scheduled workflows with optional filtering.

```bash
# List all schedules
grpcurl -d '{}' localhost:50051 loom.v1.LoomService/ListScheduledWorkflows

# List only enabled schedules
grpcurl -d '{
  "enabled_only": true
}' localhost:50051 loom.v1.LoomService/ListScheduledWorkflows
```

### DeleteScheduledWorkflow

Delete an RPC-created schedule. **Note**: YAML-sourced schedules cannot be deleted via RPC.

```bash
grpcurl -d '{
  "schedule_id": "rpc-my-workflow-1735516800-123456789"
}' localhost:50051 loom.v1.LoomService/DeleteScheduledWorkflow
```

### TriggerScheduledWorkflow

Manually trigger a scheduled workflow immediately, bypassing the cron schedule.

```bash
grpcurl -d '{
  "schedule_id": "daily-report-abc123",
  "skip_if_running": true,
  "variables": {"force_refresh": "true"}
}' localhost:50051 loom.v1.LoomService/TriggerScheduledWorkflow
```

### PauseSchedule / ResumeSchedule

Temporarily disable or re-enable a schedule without deleting it.

```bash
# Pause
grpcurl -d '{
  "schedule_id": "daily-report-abc123"
}' localhost:50051 loom.v1.LoomService/PauseSchedule

# Resume
grpcurl -d '{
  "schedule_id": "daily-report-abc123"
}' localhost:50051 loom.v1.LoomService/ResumeSchedule
```

### GetScheduleHistory

View execution history with timing and status information.

```bash
grpcurl -d '{
  "schedule_id": "daily-report-abc123",
  "limit": 20
}' localhost:50051 loom.v1.LoomService/GetScheduleHistory
```

**Response**:
```json
{
  "executions": [
    {
      "execution_id": "exec-abc-123",
      "started_at": "1704067200",
      "completed_at": "1704067215",
      "status": "success",
      "duration_ms": "15000"
    }
  ]
}
```

## Hot-Reload Functionality

The scheduler automatically detects changes to workflow YAML files and reloads them without restarting the server.

### How It Works

1. **File Monitoring**: Scheduler scans `workflow_dir` every 500ms for changes
2. **Change Detection**: Uses SHA256 hashing to detect file modifications
3. **Automatic Reload**: When a file changes:
   - Re-parses the YAML
   - Updates cron schedule
   - Recalculates next execution time
   - Preserves execution history

### Example Workflow

```bash
# 1. Create initial workflow
cat > ~/.loom/workflows/report.yaml << 'EOF'
schedule:
  cron: "0 8 * * *"  # 8 AM daily
EOF

# 2. Server detects and loads
# [INFO] Loading workflow file path=report.yaml new=true
# [INFO] Created schedule from YAML schedule_id=daily-report-abc123

# 3. Edit the file
vim ~/.loom/workflows/report.yaml
# Change cron to "0 9 * * *"

# 4. Server detects change
# [INFO] File changed path=report.yaml
# [INFO] Updated schedule from YAML schedule_id=daily-report-abc123

# 5. Delete the file
rm ~/.loom/workflows/report.yaml

# 6. Server removes schedule
# [INFO] File deleted path=report.yaml
# [INFO] Removed schedule schedule_id=daily-report-abc123
```

**Important**: YAML-sourced schedules are **read-only via RPC** to prevent conflicts with hot-reload. To modify YAML-sourced schedules, edit the YAML file directly.

## Monitoring & Observability

### Execution Statistics

Each schedule tracks comprehensive metrics:

```bash
grpcurl -d '{
  "schedule_id": "daily-report-abc123"
}' localhost:50051 loom.v1.LoomService/GetScheduledWorkflow
```

**Metrics Included**:
- **total_executions**: Total number of executions
- **successful_executions**: Number of successful completions
- **failed_executions**: Number of failed executions
- **skipped_executions**: Number of times skipped (previous run still running)
- **last_status**: "success", "failed", or "skipped"
- **last_error**: Error message if last execution failed
- **last_execution_at**: Timestamp of last execution
- **next_execution_at**: Timestamp of next scheduled execution

### Execution History

View detailed history of past executions:

```bash
grpcurl -d '{
  "schedule_id": "daily-report-abc123",
  "limit": 50
}' localhost:50051 loom.v1.LoomService/GetScheduleHistory
```

**Returns**:
- Execution ID
- Start/completion timestamps
- Status (success/failed/skipped)
- Duration in milliseconds
- Error message (if failed)

### Server Logs

Monitor scheduler activity via server logs:

```bash
looms serve

# Sample log output:
# [INFO] Workflow scheduler started workflow_dir=/Users/you/.loom/workflows hot_reload=true
# [INFO] Loading workflow file path=daily-report.yaml new=true
# [INFO] Created schedule from YAML schedule_id=daily-report-abc123
# [INFO] Executing scheduled workflow schedule_id=daily-report-abc123 workflow=daily-sales-report
# [INFO] Workflow execution completed schedule_id=daily-report-abc123 duration=12.5s status=success
```

## Best Practices

### 1. Use `skip_if_running` for Data Pipelines

Always enable `skip_if_running: true` for workflows that modify data to prevent concurrent executions and data conflicts.

```yaml
schedule:
  cron: "0 * * * *"
  skip_if_running: true  # Prevents data corruption
```

**Why**: If a workflow takes longer than expected (e.g., 65 minutes for an hourly job), the next execution will be skipped rather than running concurrently, which could corrupt data or cause race conditions.

### 2. Set Appropriate Timeouts

- **Quick tasks** (< 5 min): Set `max_execution_seconds` to detect hung workflows early
- **Long-running tasks** (> 1 hour): Increase timeout appropriately
- **Default**: 3600 seconds (1 hour)

```yaml
schedule:
  # Quick monitoring task
  max_execution_seconds: 120  # 2 minutes

  # VS.

  # Weekly backup task
  max_execution_seconds: 21600  # 6 hours
```

### 3. Choose Timezone Carefully

- **UTC**: Best for global services and consistency across regions
- **Local timezone**: Best for business-hour schedules (e.g., reports, notifications)

```yaml
# Global service - use UTC
schedule:
  cron: "0 */6 * * *"
  timezone: "UTC"

# Business reports - use local timezone
schedule:
  cron: "0 8 * * *"
  timezone: "America/New_York"  # 8 AM Eastern
```

### 4. Use Variables for Configuration

Pass configuration via `variables` rather than hardcoding in prompts:

```yaml
schedule:
  variables:
    environment: "production"
    alert_threshold: "80"
    recipient_list: "ops-team@company.com"
```

This makes workflows reusable and easier to test by changing variables.

### 5. Monitor Execution History

Regularly review execution history to identify:

- **Failed executions**: Investigate root causes
- **Skipped executions**: May indicate timing issues or slow workflows
- **Duration trends**: Detect performance degradation over time

```bash
# Weekly review
grpcurl -d '{
  "schedule_id": "daily-report-abc123",
  "limit": 100
}' localhost:50051 loom.v1.LoomService/GetScheduleHistory \
  | jq '.executions[] | select(.status != "success")'
```

### 6. Test with Manual Triggers

Before relying on cron schedule, test with manual trigger to verify workflow executes correctly:

```bash
# Test execution
grpcurl -d '{
  "schedule_id": "daily-report-abc123"
}' localhost:50051 loom.v1.LoomService/TriggerScheduledWorkflow

# Monitor execution
grpcurl -d '{
  "schedule_id": "daily-report-abc123",
  "limit": 1
}' localhost:50051 loom.v1.LoomService/GetScheduleHistory
```

### 7. Version Control YAML Files

Store workflow YAML files in git for:

- **Change history**: Track who changed what and when
- **Peer review**: Review schedules before deployment
- **Rollback capability**: Revert problematic changes
- **Documentation**: Document schedule rationale in commit messages

```bash
# Git workflow
cd ~/.loom/workflows
git init
git add daily-report.yaml
git commit -m "Add daily sales report at 8 AM Eastern

Stakeholder request: Daily reports needed before 9 AM meetings.
Estimated runtime: 10-15 minutes.
Alert ops-team if failures occur."
```

### 8. Start with Conservative Schedules

Begin with less frequent schedules and increase frequency as confidence grows:

```yaml
# Phase 1: Daily testing
cron: "0 8 * * *"

# Phase 2: Increase to hourly after 1 week success
cron: "0 * * * *"

# Phase 3: Increase to every 15 minutes after 1 month success
cron: "*/15 * * * *"
```

### 9. Use Descriptive Workflow Names

Include schedule frequency in workflow names for clarity:

```yaml
# Good
metadata:
  name: hourly-customer-sync
  name: daily-sales-report
  name: weekly-full-backup

# Avoid
metadata:
  name: sync-job
  name: report
  name: backup
```

### 10. Document Schedule Rationale

Add comments explaining why schedules are configured as they are:

```yaml
schedule:
  # Run at 2 AM to avoid business hours (8 AM - 6 PM)
  # Database backup window: 1 AM - 4 AM
  cron: "0 2 * * *"

  # 6-hour timeout needed for 500GB+ database
  max_execution_seconds: 21600
```

## Troubleshooting

### Schedule Not Executing

**Symptom**: Schedule shows enabled but workflow never executes.

**Diagnosis**:
```bash
# 1. Check if scheduler is enabled
grpcurl -d '{
  "schedule_id": "your-schedule-id"
}' localhost:50051 loom.v1.LoomService/GetScheduledWorkflow

# 2. Check next execution time
# Verify next_execution_at is in the future

# 3. Check server logs
# Look for: "Workflow scheduler started"
# Look for: "Executing scheduled workflow"
```

**Solutions**:
- Verify `schedule.enabled = true`
- Check `next_execution_at` is valid and in the future
- Verify scheduler is configured in looms.yaml
- Check cron expression syntax: `cron.ParseStandard("0 8 * * *")`

### Execution Keeps Getting Skipped

**Symptom**: `skipped_executions` counter increasing.

**Cause**: Previous execution still running when next execution is due.

**Solutions**:

1. **Increase execution window**: Space out executions more
   ```yaml
   # Before: Every hour
   cron: "0 * * * *"

   # After: Every 2 hours
   cron: "0 */2 * * *"
   ```

2. **Increase timeout**: Allow more time for completion
   ```yaml
   max_execution_seconds: 7200  # 2 hours instead of 1
   ```

3. **Optimize workflow**: Improve execution performance
   - Parallelize stages where possible
   - Reduce data processing volume
   - Add indexes to database queries

### Schedule Not Hot-Reloading

**Symptom**: YAML file changes don't take effect.

**Diagnosis**:
```bash
# 1. Verify hot_reload enabled
grep -A5 "scheduler:" ~/.loom/looms.yaml

# 2. Check file permissions
ls -la ~/.loom/workflows/

# 3. Check YAML syntax
yamllint ~/.loom/workflows/your-workflow.yaml

# 4. Check server logs for parse errors
# Look for: "Failed to parse workflow YAML"
```

**Solutions**:
- Verify `hot_reload: true` in looms.yaml
- Check file has read permissions
- Validate YAML syntax is correct
- Check server logs for specific error messages

### Cron Expression Not Working

**Symptom**: Schedule executes at wrong times or not at all.

**Common Mistakes**:

```yaml
# Wrong: 6-field format (includes seconds)
cron: "0 0 8 * * *"  # ❌ Will fail

# Correct: 5-field format
cron: "0 8 * * *"    # ✅ Daily at 8 AM

# Wrong: Inverted day of week
cron: "0 8 * * 0-6"  # Sunday=0, Saturday=6

# Correct: Monday-Friday
cron: "0 8 * * 1-5"  # ✅ Weekdays only
```

**Testing Cron Expressions**:
```bash
# Use online validators
# https://crontab.guru/

# Or test in Go
go run -
package main
import "github.com/robfig/cron/v3"
func main() {
    _, err := cron.ParseStandard("0 8 * * *")
    println(err)
}
```

### Execution History Empty

**Symptom**: `GetScheduleHistory` returns no executions despite schedule running.

**Cause**: Execution history is only recorded after workflows complete.

**Verification**:
```bash
# Check if workflow actually executed
grpcurl -d '{
  "schedule_id": "your-schedule-id"
}' localhost:50051 loom.v1.LoomService/GetScheduledWorkflow

# Check stats
# - total_executions should be > 0
# - last_execution_at should have recent timestamp
```

### Timezone Issues

**Symptom**: Schedule executes at unexpected times.

**Common Issues**:

```yaml
# Wrong: Abbreviations not supported
timezone: "EST"  # ❌ Will fail or use wrong timezone

# Correct: IANA timezone names
timezone: "America/New_York"  # ✅ Handles DST correctly

# Wrong: Offset notation
timezone: "UTC-5"  # ❌ Not supported

# Correct: Named timezone
timezone: "America/Chicago"  # ✅ UTC-6 (or UTC-5 during DST)
```

**Testing Timezones**:
```bash
# List available timezones
timedatectl list-timezones | grep America

# Test timezone conversion
TZ="America/New_York" date
TZ="Europe/London" date
```

## Examples

See `examples/scheduled-workflows/` for complete examples:

- **daily-report.yaml**: Daily workflow at 8 AM Eastern Time
- **hourly-sync.yaml**: Hourly data synchronization with skip_if_running
- **weekly-backup.yaml**: Weekly backup with long timeout (6 hours)
- **frequent-monitor.yaml**: Every 5 minutes monitoring with short timeout

Each example includes:
- Complete workflow definition
- Schedule configuration
- Inline documentation
- Best practices

## See Also

- [Workflow Patterns Guide](../patterns/) - Learn about pipeline, fork-join, and other patterns
- [Agent Configuration](../agents/) - Configure agents used in workflows
- [gRPC API Reference](../../proto/loom/v1/loom.proto) - Complete API documentation
- [Examples Directory](../../examples/scheduled-workflows/) - Working examples
