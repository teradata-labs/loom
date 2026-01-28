# Scheduled Workflows Examples

This directory contains example workflow YAML files demonstrating the **cron-based workflow scheduling** feature in Loom.

## Overview

Scheduled workflows enable automatic execution of workflows based on cron expressions. The scheduler:
- ✅ **Persists** schedules in SQLite (survives restarts)
- ✅ **Hot-reloads** YAML files automatically when changed
- ✅ **Prevents** concurrent execution with `skip_if_running`
- ✅ **Supports** timezone-aware scheduling
- ✅ **Tracks** execution history and statistics

## Examples

### 1. Daily Report (`daily-report.yaml`)
**Schedule**: Every day at 8:00 AM Eastern Time
**Cron**: `0 8 * * *`
**Use Case**: Generate and distribute daily sales reports to stakeholders

**Key Features**:
- Timezone-aware (America/New_York)
- Passes workflow variables for customization
- 30-minute execution timeout

### 2. Hourly Sync (`hourly-sync.yaml`)
**Schedule**: Every hour at minute 0
**Cron**: `0 * * * *`
**Use Case**: Synchronize customer data from CRM to data warehouse

**Key Features**:
- UTC timezone for consistency
- `skip_if_running: true` to prevent data conflicts
- 45-minute execution window

### 3. Weekly Backup (`weekly-backup.yaml`)
**Schedule**: Every Sunday at midnight UTC
**Cron**: `0 0 * * 0`
**Use Case**: Full backup of production systems and databases

**Key Features**:
- Long execution timeout (6 hours)
- Comprehensive backup validation
- Detailed configuration via variables

### 4. Frequent Monitor (`frequent-monitor.yaml`)
**Schedule**: Every 5 minutes
**Cron**: `*/5 * * * *`
**Use Case**: Monitor system health and alert on anomalies

**Key Features**:
- High frequency execution
- Short timeout (2 minutes)
- Quick alerting on issues

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

## Usage

### Option 1: YAML File (Recommended)

1. **Create workflow YAML** with `schedule:` section:
   ```bash
   mkdir -p $LOOM_DATA_DIR/workflows
   cp examples/scheduled-workflows/daily-report.yaml $LOOM_DATA_DIR/workflows/
   ```

2. **Enable scheduler** in `$LOOM_DATA_DIR/looms.yaml`:
   ```yaml
   scheduler:
     enabled: true
     workflow_dir: "$LOOM_DATA_DIR/workflows"
     hot_reload: true
   ```

3. **Start server**:
   ```bash
   looms serve
   ```

4. **Monitor schedules**:
   ```bash
   # List all schedules
   grpcurl -d '{}' localhost:50051 loom.v1.LoomService/ListScheduledWorkflows

   # Get schedule details
   grpcurl -d '{"schedule_id":"daily-report-abc123"}' \
     localhost:50051 loom.v1.LoomService/GetScheduledWorkflow

   # View execution history
   grpcurl -d '{"schedule_id":"daily-report-abc123","limit":10}' \
     localhost:50051 loom.v1.LoomService/GetScheduleHistory
   ```

### Option 2: gRPC API

Create schedules programmatically via RPC:

```bash
# Create schedule
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
    "skip_if_running": true
  }
}' localhost:50051 loom.v1.LoomService/ScheduleWorkflow

# Manually trigger execution
grpcurl -d '{"schedule_id":"rpc-my-workflow-1234567890"}' \
  localhost:50051 loom.v1.LoomService/TriggerScheduledWorkflow

# Pause schedule
grpcurl -d '{"schedule_id":"rpc-my-workflow-1234567890"}' \
  localhost:50051 loom.v1.LoomService/PauseSchedule

# Resume schedule
grpcurl -d '{"schedule_id":"rpc-my-workflow-1234567890"}' \
  localhost:50051 loom.v1.LoomService/ResumeSchedule
```

## Hot-Reload

The scheduler automatically detects changes to workflow YAML files:

```bash
# Edit workflow
vim $LOOM_DATA_DIR/workflows/daily-report.yaml

# Change cron from "0 8 * * *" to "0 9 * * *"
# Save file

# Scheduler automatically:
# 1. Detects file change (via SHA256 hash)
# 2. Reloads workflow configuration
# 3. Updates cron schedule
# 4. Logs: "Updated schedule from YAML"
```

**Note**: YAML-sourced schedules are **read-only via RPC** to prevent conflicts with hot-reload.

## Schedule Management

### YAML-Sourced vs RPC-Created Schedules

| Feature | YAML-Sourced | RPC-Created |
|---------|-------------|-------------|
| Source | `$LOOM_DATA_DIR/workflows/*.yaml` | gRPC API calls |
| Hot-reload | ✅ Yes | ❌ No |
| Modifiable via RPC | ❌ No (edit YAML) | ✅ Yes |
| Deletable via RPC | ❌ No (delete YAML) | ✅ Yes |
| Persistence | File + SQLite | SQLite only |
| Best for | Static schedules, version control | Dynamic schedules, runtime management |

### Viewing Logs

```bash
# Server logs show schedule activity
looms serve

# Sample log output:
# [INFO] Loading workflow file path=$LOOM_DATA_DIR/workflows/daily-report.yaml new=true
# [INFO] Created schedule from YAML schedule_id=daily-report-abc123 path=$LOOM_DATA_DIR/workflows/daily-report.yaml
# [INFO] Workflow scheduler started workflow_dir=$LOOM_DATA_DIR/workflows hot_reload=true
# [INFO] Executing scheduled workflow schedule_id=daily-report-abc123 workflow=daily-sales-report
# [INFO] Workflow execution completed schedule_id=daily-report-abc123 duration=12.5s status=success
```

## Monitoring & Observability

### Execution Statistics

Each schedule tracks:
- **Total executions**: Total number of times executed
- **Successful executions**: Number of successful completions
- **Failed executions**: Number of failed executions
- **Skipped executions**: Number of times skipped (due to previous run still running)
- **Last status**: "success", "failed", or "skipped"
- **Last error**: Error message if last execution failed
- **Last execution time**: Timestamp of last execution
- **Next execution time**: Timestamp of next scheduled execution

### Execution History

View detailed history of past executions:

```bash
grpcurl -d '{"schedule_id":"daily-report-abc123","limit":20}' \
  localhost:50051 loom.v1.LoomService/GetScheduleHistory
```

Returns:
```json
{
  "executions": [
    {
      "execution_id": "exec-abc-123",
      "started_at": "1704067200",
      "completed_at": "1704067215",
      "status": "success",
      "duration_ms": "15000"
    },
    ...
  ]
}
```

## Best Practices

### 1. Use `skip_if_running` for Data Pipelines
Always enable `skip_if_running: true` for workflows that modify data to prevent concurrent executions and data conflicts.

### 2. Set Appropriate Timeouts
- **Quick tasks** (< 5 min): Set `max_execution_seconds` to prevent hung workflows
- **Long-running tasks** (> 1 hour): Increase timeout appropriately
- **Default**: 3600 seconds (1 hour)

### 3. Choose Timezone Carefully
- **UTC**: Best for global services and consistency
- **Local timezone**: Best for business-hour schedules (e.g., "America/New_York" for US East Coast)

### 4. Use Variables for Configuration
Pass configuration via `variables` rather than hardcoding in prompts:
```yaml
schedule:
  variables:
    environment: "production"
    alert_threshold: "80"
```

### 5. Monitor Execution History
Regularly review execution history to identify:
- Failed executions that need investigation
- Skipped executions indicating timing issues
- Execution duration trends

### 6. Test with Manual Triggers
Before relying on cron schedule, test with manual trigger:
```bash
grpcurl -d '{"schedule_id":"daily-report-abc123"}' \
  localhost:50051 loom.v1.LoomService/TriggerScheduledWorkflow
```

### 7. Version Control YAML Files
Store workflow YAML files in git for:
- Change history
- Peer review
- Rollback capability
- Documentation

## Troubleshooting

### Schedule Not Executing

1. **Check if enabled**:
   ```bash
   grpcurl -d '{"schedule_id":"your-schedule-id"}' \
     localhost:50051 loom.v1.LoomService/GetScheduledWorkflow
   # Verify: schedule.enabled = true
   ```

2. **Check next execution time**:
   ```bash
   # next_execution_at should be in the future
   # If in the past, check if scheduler is running
   ```

3. **Check server logs**:
   ```bash
   # Look for: "Workflow scheduler started"
   # Look for: "Executing scheduled workflow"
   ```

### Execution Keeps Getting Skipped

If `skipped_executions` is increasing:
- Previous execution is taking too long
- Increase `max_execution_seconds`
- Or optimize workflow performance

### Schedule Not Hot-Reloading

1. Verify `hot_reload: true` in config
2. Check file permissions (scheduler needs read access)
3. Verify YAML syntax is valid
4. Check server logs for parse errors

## See Also

- [Architecture Documentation](../../website/content/en/docs/architecture)
- [Workflow Patterns Guide](../config/workflows)
- [Agent Configuration](../config/agents)
- [gRPC API Reference](../../proto/loom/v1/loom.proto)
