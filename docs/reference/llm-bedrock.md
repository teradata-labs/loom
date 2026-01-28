
# AWS Bedrock Integration

Complete reference for connecting Loom to AWS Bedrock for Claude models.

**Version**: v1.0.0-beta.2


## Table of Contents

1. [Quick Reference](#quick-reference)
2. [Overview](#overview)
3. [Prerequisites](#prerequisites)
4. [Authentication Methods](#authentication-methods)
5. [Configuration](#configuration)
6. [Available Models](#available-models)
7. [Available Regions](#available-regions)
8. [Testing Your Setup](#testing-your-setup)
9. [Cost Estimation](#cost-estimation)
10. [Security Best Practices](#security-best-practices)
11. [Advanced Configuration](#advanced-configuration)
12. [Monitoring and Observability](#monitoring-and-observability)
13. [Error Codes](#error-codes)
14. [Comparison: Bedrock vs Direct Anthropic](#comparison-bedrock-vs-direct-anthropic)
15. [See Also](#see-also)


## Quick Reference

### Configuration Summary

```yaml
llm:
  provider: bedrock
  bedrock_region: us-west-2
  bedrock_model_id: anthropic.claude-sonnet-4-5-20250929-v1:0
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60
```

### Available Models

| Model | Bedrock Model ID | Context | Best For |
|-------|------------------|---------|----------|
| **Claude Sonnet 4.5** | `anthropic.claude-sonnet-4-5-20250929-v1:0` | 200k | Latest, best performance (recommended) |
| **Claude Haiku 4.5** | `anthropic.claude-haiku-4-5-20250128-v1:0` | 200k | Fast, cost-effective |
| **Claude Opus 4.1** | `anthropic.claude-opus-4-1-20250514-v1:0` | 200k | Maximum intelligence |
| **Claude 3.5 Sonnet v2** | `anthropic.claude-3-5-sonnet-20241022-v2:0` | 200k | Previous generation |
| **Claude 3.5 Sonnet** | `anthropic.claude-3-5-sonnet-20240620-v1:0` | 200k | Legacy |
| **Claude 3 Opus** | `anthropic.claude-3-opus-20240229-v1:0` | 200k | Legacy maximum intelligence |
| **Claude 3 Haiku** | `anthropic.claude-3-haiku-20240307-v1:0` | 200k | Legacy fast model |

### Authentication Methods (Priority Order)

| Method | When to Use | Configuration |
|--------|-------------|---------------|
| **IAM Role** | EC2/ECS/Lambda (recommended) | No config needed - automatic |
| **AWS Profile** | Local development with named profile | `bedrock_profile: my-profile` |
| **Environment Variables** | CI/CD, containers | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` |
| **Keyring** | Local development with explicit credentials | `looms config set-key bedrock_access_key_id` |
| **CLI Flags** | Testing only (not recommended) | `--bedrock-access-key`, `--bedrock-secret-key` |

### Supported Regions

| Region | Code | Latency (US East) |
|--------|------|-------------------|
| **US West (Oregon)** | `us-west-2` | ~40ms (recommended) |
| **US East (N. Virginia)** | `us-east-1` | ~5ms |
| **Europe (Frankfurt)** | `eu-central-1` | ~90ms |
| **Asia Pacific (Tokyo)** | `ap-northeast-1` | ~150ms |
| **Asia Pacific (Singapore)** | `ap-southeast-1` | ~180ms |

### Pricing (US East - us-east-1)

| Model | Input (per 1M tokens) | Output (per 1M tokens) | Typical Task* |
|-------|----------------------|------------------------|---------------|
| **Sonnet 4.5** | $3.00 | $15.00 | $0.018 |
| **Haiku 4.5** | $0.25 | $1.25 | $0.0015 |
| **Opus 4.1** | $15.00 | $75.00 | $0.090 |

\* Typical task = 500 input tokens, 1000 output tokens

### Common Commands

```bash
# Authentication check
aws sts get-caller-identity

# List available Claude models
aws bedrock list-foundation-models --region us-east-1 \
  --query "modelSummaries[?contains(modelId, 'claude')].modelId"

# Store credentials in keyring
looms config set-key bedrock_access_key_id
looms config set-key bedrock_secret_access_key

# Configure Bedrock
looms config set llm.bedrock_region us-west-2
looms config set llm.bedrock_model_id anthropic.claude-sonnet-4-5-20250929-v1:0

# Test connection
grpcurl -plaintext -d '{"query": "Hello from Bedrock!"}' \
  localhost:50051 loom.v1.LoomService/Weave
```


## Overview

AWS Bedrock provides Claude models through AWS infrastructure:
- Enterprise-grade security and compliance
- AWS IAM integration
- VPC and PrivateLink support
- Unified AWS billing
- Regional availability

**Use Bedrock when**:
- You need AWS ecosystem integration
- You require VPC/PrivateLink isolation
- You want unified AWS billing
- You need enterprise compliance (HIPAA, SOC 2, etc.)


## Prerequisites

1. **AWS Account**: Active AWS account with Bedrock access
2. **Model Access**: Request access to Claude models in AWS Console
3. **IAM Permissions**: Appropriate IAM permissions for Bedrock
4. **AWS Region**: Choose a region where Bedrock is available

### Required IAM Permissions

Your IAM user/role needs:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream"
      ],
      "Resource": "arn:aws:bedrock:*::foundation-model/anthropic.claude-*"
    }
  ]
}
```

### Request Model Access

1. Go to AWS Console → Bedrock
2. Click "Model access" in left sidebar
3. Click "Request model access"
4. Select Anthropic models (Claude 3.5 Sonnet, etc.)
5. Accept terms and submit request
6. Wait for approval (usually instant for Claude models)

**Verify access**:
```bash
aws bedrock list-foundation-models --region us-east-1 \
  --query "modelSummaries[?contains(modelId, 'claude')]" \
  --output table
```


## Authentication Methods

Bedrock supports multiple authentication methods (in order of priority):

### Method 1: IAM Role (Recommended for EC2/ECS/Lambda)

No configuration needed - Loom automatically uses the instance/task IAM role:

```yaml
llm:
  provider: bedrock
  bedrock_region: us-west-2
  bedrock_model_id: anthropic.claude-sonnet-4-5-20250929-v1:0
  # No credentials needed - uses IAM role
```

**When to use**:
- EC2 instances
- ECS/Fargate tasks
- Lambda functions
- Any AWS compute with IAM role

**Advantages**:
- No credentials to manage
- Automatic credential rotation
- Audit trail via CloudTrail


### Method 2: AWS Profile

Use a named profile from `~/.aws/credentials`:

```yaml
llm:
  provider: bedrock
  bedrock_region: us-west-2
  bedrock_profile: my-profile
  bedrock_model_id: anthropic.claude-sonnet-4-5-20250929-v1:0
```

Your `~/.aws/credentials`:
```ini
[my-profile]
aws_access_key_id = AKIAIOSFODNN7EXAMPLE
aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

**When to use**:
- Local development
- Multiple AWS accounts
- Switching between projects


### Method 3: Environment Variables

```bash
export AWS_ACCESS_KEY_ID="AKIAIOSFODNN7EXAMPLE"
export AWS_SECRET_ACCESS_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
export AWS_REGION="us-west-2"
```

**When to use**:
- CI/CD pipelines
- Docker containers
- Serverless deployments


### Method 4: Keyring (for Explicit Credentials)

For security, store AWS credentials in system keyring:

```bash
# Store access key ID
looms config set-key bedrock_access_key_id

# Store secret access key
looms config set-key bedrock_secret_access_key

# (Optional) Store session token for temporary credentials
looms config set-key bedrock_session_token
```

Then in config:
```yaml
llm:
  provider: bedrock
  bedrock_region: us-west-2
  bedrock_model_id: anthropic.claude-sonnet-4-5-20250929-v1:0
  # Keys loaded from keyring automatically
```

**When to use**:
- Local development without AWS CLI
- Explicit credential management
- Sharing workstation with multiple users


### Method 5: CLI Flags (Not Recommended)

```bash
looms serve --bedrock-access-key "AKIAIOSFO..." --bedrock-secret-key "wJalrX..."
```

**Warning**: Only use for testing - prefer IAM roles or keyring!

**When to use**:
- Quick testing only
- Never in production


## Configuration

### Option 1: CLI Commands (Recommended for Novices)

Configure Bedrock using simple CLI commands (no YAML editing required):

```bash
# Initialize config and choose Bedrock when prompted
looms config init

# Set Bedrock-specific values
looms config set llm.bedrock_region us-west-2
looms config set llm.bedrock_model_id anthropic.claude-sonnet-4-5-20250929-v1:0

# Optional: Set AWS profile (or use IAM role)
looms config set llm.bedrock_profile default

# Optional: Store credentials in keyring (if not using IAM role/profile)
looms config set-key bedrock_access_key_id
looms config set-key bedrock_secret_access_key
```


### Option 2: Direct YAML Editing

Edit `$LOOM_DATA_DIR/looms.yaml` directly:

```yaml
llm:
  provider: bedrock

  # Required
  bedrock_region: us-west-2
  bedrock_model_id: anthropic.claude-sonnet-4-5-20250929-v1:0

  # Optional - choose ONE authentication method:
  # bedrock_profile: default           # Use AWS profile
  # bedrock_access_key_id: ...         # From keyring/env
  # bedrock_secret_access_key: ...     # From keyring/env
  # bedrock_session_token: ...         # For temporary credentials

  # Common parameters
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60
```


### Configuration Parameters

#### provider

**Type**: `string`
**Required**: Yes
**Value**: `bedrock`

Set this to `bedrock` to use AWS Bedrock.


#### bedrock_region

**Type**: `string`
**Required**: Yes
**Allowed values**: `us-west-2`, `us-east-1`, `eu-central-1`, `ap-northeast-1`, `ap-southeast-1`

AWS region where Bedrock is deployed.

**Example**:
```yaml
bedrock_region: us-west-2
```

**See**: [Available Regions](#available-regions)


#### bedrock_model_id

**Type**: `string`
**Required**: Yes

Bedrock model identifier for Claude.

**Examples**:
- `anthropic.claude-sonnet-4-5-20250929-v1:0` - Claude Sonnet 4.5 (recommended)
- `anthropic.claude-haiku-4-5-20250128-v1:0` - Claude Haiku 4.5
- `anthropic.claude-opus-4-1-20250514-v1:0` - Claude Opus 4.1

**See**: [Available Models](#available-models)


#### bedrock_profile

**Type**: `string`
**Required**: No
**Default**: None

AWS profile name from `~/.aws/credentials`.

**Example**:
```yaml
bedrock_profile: default
```

**When to use**: Local development with AWS CLI configured


#### bedrock_access_key_id

**Type**: `string`
**Required**: No
**Default**: None
**Source**: Keyring, environment variable, or explicit value

AWS access key ID.

**Recommended**: Store in keyring via `looms config set-key bedrock_access_key_id`

**Environment variable**: `AWS_ACCESS_KEY_ID`


#### bedrock_secret_access_key

**Type**: `string`
**Required**: No
**Default**: None
**Source**: Keyring, environment variable, or explicit value

AWS secret access key.

**Recommended**: Store in keyring via `looms config set-key bedrock_secret_access_key`

**Environment variable**: `AWS_SECRET_ACCESS_KEY`


#### bedrock_session_token

**Type**: `string`
**Required**: No
**Default**: None
**Source**: Keyring, environment variable, or explicit value

AWS session token for temporary credentials (STS).

**When to use**: Temporary credentials from `aws sts assume-role`

**Environment variable**: `AWS_SESSION_TOKEN`


#### temperature

**Type**: `float64`
**Default**: `1.0`
**Range**: `0.0` - `1.0`
**Required**: No

Sampling temperature for creativity control.

**Temperature guide**:
- **0.0-0.3**: Deterministic, focused responses
- **0.7-1.0**: Balanced creativity (recommended)

**Example**:
```yaml
temperature: 1.0
```


#### max_tokens

**Type**: `int`
**Default**: `4096`
**Range**: `1` - `200000` (model-dependent)
**Required**: No

Maximum response length in tokens.

**Example**:
```yaml
max_tokens: 4096
```


#### timeout_seconds

**Type**: `int`
**Default**: `60`
**Range**: `1` - `600`
**Required**: No

Request timeout in seconds.

**Example**:
```yaml
timeout_seconds: 60
```


## Available Models

| Model | Bedrock Model ID | Context | Best For |
|-------|------------------|---------|----------|
| **Claude Sonnet 4.5** | `anthropic.claude-sonnet-4-5-20250929-v1:0` | 200k | Latest, best performance (recommended) |
| **Claude Haiku 4.5** | `anthropic.claude-haiku-4-5-20250128-v1:0` | 200k | Fast, cost-effective |
| **Claude Opus 4.1** | `anthropic.claude-opus-4-1-20250514-v1:0` | 200k | Maximum intelligence, complex reasoning |
| **Claude 3.5 Sonnet v2** | `anthropic.claude-3-5-sonnet-20241022-v2:0` | 200k | Previous generation |
| **Claude 3.5 Sonnet** | `anthropic.claude-3-5-sonnet-20240620-v1:0` | 200k | Legacy |
| **Claude 3 Opus** | `anthropic.claude-3-opus-20240229-v1:0` | 200k | Legacy maximum intelligence |
| **Claude 3 Haiku** | `anthropic.claude-3-haiku-20240307-v1:0` | 200k | Legacy fast model |

**List available models**:
```bash
aws bedrock list-foundation-models --region us-east-1 \
  --query "modelSummaries[?contains(modelId, 'claude')].modelId"
```


## Available Regions

Claude on Bedrock is available in:

| Region | Code | Description | Latency (from US East) |
|--------|------|-------------|------------------------|
| **US West (Oregon)** | `us-west-2` | Recommended for most US users | ~40ms |
| **US East (N. Virginia)** | `us-east-1` | Lowest latency for US East Coast | ~5ms |
| **Europe (Frankfurt)** | `eu-central-1` | EU data residency | ~90ms |
| **Asia Pacific (Tokyo)** | `ap-northeast-1` | Japan, Asia Pacific | ~150ms |
| **Asia Pacific (Singapore)** | `ap-southeast-1` | Southeast Asia | ~180ms |

Check [AWS Bedrock documentation](https://docs.aws.amazon.com/bedrock/latest/userguide/models-regions.html) for current availability.

**Select nearest region** for lowest latency:
```yaml
# US East Coast
bedrock_region: us-east-1

# US West Coast
bedrock_region: us-west-2

# Europe
bedrock_region: eu-central-1

# Asia Pacific
bedrock_region: ap-northeast-1  # Tokyo
# or
bedrock_region: ap-southeast-1  # Singapore
```


## Testing Your Setup

### Step 1: Verify IAM Permissions

```bash
# Check your AWS identity
aws sts get-caller-identity
```

**Expected output**:
```json
{
    "UserId": "AIDAI...",
    "Account": "123456789012",
    "Arn": "arn:aws:iam::123456789012:user/myuser"
}
```


### Step 2: Verify Model Access

```bash
aws bedrock list-foundation-models \
  --region us-east-1 \
  --query "modelSummaries[?contains(modelId, 'claude')]" \
  --output table
```

**Expected output**: List of Claude models

If empty, see [ERR_ACCESS_DENIED](#err_access_denied).


### Step 3: Start Loom Server

```bash
looms serve
```

**Expected output**:
```
INFO  Starting Loom server
INFO  gRPC server listening on :50051
INFO  HTTP gateway listening on :8080
INFO  LLM provider: bedrock (region: us-west-2)
INFO  Model: anthropic.claude-sonnet-4-5-20250929-v1:0
```


### Step 4: Test with gRPC

```bash
grpcurl -plaintext -d '{"query": "Hello from Bedrock!"}' \
  localhost:50051 loom.v1.LoomService/Weave
```

**Expected output**:
```json
{
  "text": "Hello! I'm Claude, running on AWS Bedrock. How can I help you today?",
  "sessionId": "sess_abc123",
  "cost": {
    "llmCost": {
      "provider": "bedrock",
      "model": "anthropic.claude-sonnet-4-5-20250929-v1:0",
      "inputTokens": 12,
      "outputTokens": 20,
      "costUsd": 0.000336
    }
  }
}
```


## Cost Estimation

Bedrock pricing varies by region. US East (us-east-1) pricing:

### Claude Sonnet 4.5
- **Input**: $3.00 per 1M tokens
- **Output**: $15.00 per 1M tokens

### Claude Haiku 4.5
- **Input**: $0.25 per 1M tokens
- **Output**: $1.25 per 1M tokens

### Claude Opus 4.1
- **Input**: $15.00 per 1M tokens
- **Output**: $75.00 per 1M tokens

### Claude 3.5 Sonnet v2
- **Input**: $3.00 per 1M tokens
- **Output**: $15.00 per 1M tokens

### Example Costs (Claude Sonnet 4.5)

| Task | Input Tokens | Output Tokens | Cost |
|------|--------------|---------------|------|
| Simple query | 50 | 100 | $0.0018 |
| Medium task | 500 | 1000 | $0.018 |
| Large task | 5000 | 10000 | $0.18 |
| Data analysis | 50000 | 5000 | $0.225 |

**Note**: Prices vary by region. Check [AWS Bedrock Pricing](https://aws.amazon.com/bedrock/pricing/) for your region.


## Security Best Practices

1. **Use IAM Roles**: Prefer EC2/ECS instance roles over static credentials
2. **Least Privilege**: Grant only `bedrock:InvokeModel` permission
3. **Resource Restrictions**: Limit IAM policy to specific model ARNs
4. **Keyring for Secrets**: Never commit AWS credentials to config files
5. **Enable CloudTrail**: Log all Bedrock API calls for audit
6. **VPC Endpoints**: Use AWS PrivateLink for network isolation
7. **Session Tokens**: Use temporary credentials (STS AssumeRole)

### Example: Least Privilege IAM Policy

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "bedrock:InvokeModel",
      "Resource": [
        "arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-3-5-sonnet-*",
        "arn:aws:bedrock:us-west-2::foundation-model/anthropic.claude-3-5-sonnet-*"
      ]
    }
  ]
}
```

**Benefits**:
- Limits access to specific models only
- Restricts to specific regions
- No wildcard permissions


## Advanced Configuration

### Cross-Region Failover

Configure multiple regions for high availability:

```yaml
# Primary region
bedrock_region: us-east-1

# Implement failover in your infrastructure
# Loom currently uses single region
```

**Note**: Multi-region failover requires external orchestration (e.g., Route 53, load balancer).


### VPC Endpoints (PrivateLink)

For network isolation:

1. Create VPC endpoint for Bedrock in AWS Console:
   - Service name: `com.amazonaws.{region}.bedrock-runtime`
   - VPC: Select your VPC
   - Subnets: Select private subnets
   - Security groups: Allow HTTPS (443)

2. Use same config - SDK automatically uses VPC endpoint

3. No internet gateway required

**Benefits**:
- Traffic never leaves AWS network
- No public internet exposure
- Lower latency
- Enhanced security


### Temporary Credentials

Using AWS STS for temporary access:

```bash
# Get temporary credentials
aws sts assume-role --role-arn arn:aws:iam::123456789012:role/LoomRole \
  --role-session-name loom-session \
  --duration-seconds 3600
```

**Expected output**:
```json
{
  "Credentials": {
    "AccessKeyId": "ASIATEMP...",
    "SecretAccessKey": "...",
    "SessionToken": "FwoGZXIv...",
    "Expiration": "2025-01-01T12:00:00Z"
  }
}
```

**Use credentials**:
```bash
# Export credentials
export AWS_ACCESS_KEY_ID="ASIATEMP..."
export AWS_SECRET_ACCESS_KEY="..."
export AWS_SESSION_TOKEN="FwoGZXIv..."

# Or store in keyring
looms config set-key bedrock_session_token
```

**Benefits**:
- Time-limited access
- Automatic expiration
- Enhanced audit trail
- Role-based access control


## Monitoring and Observability

### CloudWatch Metrics

Bedrock automatically logs metrics to CloudWatch:
- **Invocation count**: Number of API calls
- **Latency**: P50, P90, P99 response times
- **Error rate**: Failed invocations
- **Token usage**: Input/output token consumption

**View metrics**:
```bash
aws cloudwatch get-metric-statistics \
  --namespace AWS/Bedrock \
  --metric-name Invocations \
  --dimensions Name=ModelId,Value=anthropic.claude-sonnet-4-5-20250929-v1:0 \
  --start-time 2025-01-01T00:00:00Z \
  --end-time 2025-01-01T23:59:59Z \
  --period 3600 \
  --statistics Sum
```


### CloudTrail Logging

Enable CloudTrail for audit logs:
- API call history
- IAM principal used
- Request parameters (without sensitive data)
- Response metadata

**Enable CloudTrail**:
1. Go to AWS Console → CloudTrail
2. Create trail
3. Select "Bedrock" as data event source
4. Enable logging


### Cost Tracking

Use AWS Cost Explorer with tags:

```bash
# Tag your IAM role or user
aws iam tag-user --user-name loom-user \
  --tags Key=Project,Value=Loom Key=Environment,Value=Production
```

**View costs**:
1. Go to AWS Console → Cost Explorer
2. Filter by tag: `Project=Loom`
3. Group by service: Bedrock


## Error Codes

### ERR_ACCESS_DENIED

**Code**: `access_denied`
**HTTP Status**: 403 Forbidden
**AWS Error**: `AccessDeniedException`

**Cause**: Insufficient IAM permissions or model access not granted.

**Example**:
```
Error: access_denied: User: arn:aws:iam::123456789012:user/myuser is not authorized to perform: bedrock:InvokeModel on resource: arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-sonnet-4-5-20250929-v1:0
```

**Resolution**:

**Step 1: Check IAM permissions**:
```bash
# Test your credentials
aws sts get-caller-identity

# List accessible models
aws bedrock list-foundation-models --region us-east-1
```

**Step 2: Add IAM policy**:
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream"
      ],
      "Resource": "arn:aws:bedrock:*::foundation-model/anthropic.claude-*"
    }
  ]
}
```

**Step 3: Request model access**:
1. Go to AWS Console → Bedrock → Model access
2. Select Claude models
3. Accept terms and submit

**Retry behavior**: Loom will not automatically retry. Fix permissions and retry request.


### ERR_MODEL_NOT_FOUND

**Code**: `model_not_found`
**HTTP Status**: 404 Not Found
**AWS Error**: `ResourceNotFoundException`

**Cause**: Model ID incorrect, not available in region, or access not granted.

**Example**:
```
Error: model_not_found: Could not find foundation model identifier anthropic.claude-sonnet-4-5-20250929-v1:0 in region us-east-1
```

**Resolution**:

**Option 1: Verify model ID**:
```bash
# List available models in your region
aws bedrock list-foundation-models --region us-east-1 \
  --query "modelSummaries[?contains(modelId, 'claude')].modelId"
```

**Option 2: Check region availability**:
- Model may not be available in selected region
- Try `us-west-2` or `us-east-1` (most models available)

**Option 3: Request model access**:
1. Go to AWS Console → Bedrock → Model access
2. Check if model access is "Available" (not "Pending")

**Prevention**: Always verify model ID matches AWS documentation exactly (case-sensitive).


### ERR_CREDENTIALS_NOT_LOADED

**Code**: `credentials_not_loaded`
**HTTP Status**: 401 Unauthorized
**AWS Error**: `UnrecognizedClientException`

**Cause**: Loom tried all authentication methods and failed to load credentials.

**Example**:
```
Error: credentials_not_loaded: failed to load AWS credentials from any source (IAM role, profile, environment, keyring)
```

**Resolution**:

**Step 1: Check AWS CLI**:
```bash
aws sts get-caller-identity
```

If this fails, AWS CLI is not configured.

**Step 2: Verify environment variables**:
```bash
env | grep AWS
```

Should show `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`.

**Step 3: Check AWS credentials file**:
```bash
cat ~/.aws/credentials
```

Should contain profile with access key.

**Step 4: Check keyring**:
```bash
looms config get-key bedrock_access_key_id
```

**Step 5: Test with explicit credentials**:
```bash
looms serve \
  --bedrock-access-key "YOUR_KEY" \
  --bedrock-secret-key "YOUR_SECRET" \
  --bedrock-region "us-east-1"
```

**Retry behavior**: Loom will not automatically retry. Configure credentials and retry request.


### ERR_THROTTLING

**Code**: `throttling`
**HTTP Status**: 429 Too Many Requests
**AWS Error**: `ThrottlingException`

**Cause**: Bedrock rate limit exceeded.

**Example**:
```
Error: throttling: Rate exceeded for model anthropic.claude-sonnet-4-5-20250929-v1:0 in region us-west-2
```

**Bedrock Default Quotas**:
- **Requests per minute**: 100-1000 (varies by model/region)
- **Tokens per minute**: 100,000-400,000 (varies by model/region)

**Resolution**:

**Option 1: Request quota increase**:
1. Go to AWS Console → Service Quotas
2. Search "Bedrock"
3. Select quota (e.g., "Requests per minute for Claude Sonnet 4.5")
4. Request quota increase

**Option 2: Implement exponential backoff**:
```yaml
# Loom automatically retries with backoff
# No configuration needed
```

**Option 3: Reduce request rate**:
- Add delays between requests
- Use batching where possible
- Distribute load across multiple regions

**Option 4: Use multiple regions**:
```yaml
# Deploy Loom instances in multiple regions
# Load balance across regions
```

**Retry behavior**: Loom automatically retries with exponential backoff (max 3 attempts).


### ERR_INVALID_REGION

**Code**: `invalid_region`
**HTTP Status**: 400 Bad Request
**AWS Error**: `ValidationException`

**Cause**: Claude is not available in specified AWS region.

**Example**:
```
Error: invalid_region: Invalid region 'us-west-1'. Claude is not available in this region.
```

**Resolution**:

Use supported regions:
- `us-west-2` (US West - Oregon) - **Recommended**
- `us-east-1` (US East - N. Virginia)
- `eu-central-1` (Europe - Frankfurt)
- `ap-northeast-1` (Asia Pacific - Tokyo)
- `ap-southeast-1` (Asia Pacific - Singapore)

**Fix configuration**:
```yaml
bedrock_region: us-west-2  # Change to supported region
```

**Verify region availability**:
```bash
aws bedrock list-foundation-models --region us-west-2 \
  --query "modelSummaries[?contains(modelId, 'claude')]"
```

**Prevention**: Always use regions from [Available Regions](#available-regions) section.


### ERR_TIMEOUT

**Code**: `timeout`
**HTTP Status**: 504 Gateway Timeout
**gRPC Code**: `DEADLINE_EXCEEDED`

**Cause**: Request took longer than configured timeout.

**Example**:
```
Error: timeout: request exceeded timeout of 60s (Bedrock response time: 65s)
```

**Resolution**:

**Option 1: Increase timeout**:
```yaml
timeout_seconds: 120  # 2 minutes for complex tasks
```

**Option 2: Reduce response length**:
```yaml
max_tokens: 2048  # Faster completion
```

**Option 3: Use faster model**:
```yaml
# Switch to Haiku for speed
bedrock_model_id: anthropic.claude-haiku-4-5-20250128-v1:0
```

**Option 4: Check network latency**:
```bash
# Test latency to Bedrock endpoint
time aws bedrock list-foundation-models --region us-west-2
```

**Retry behavior**: Loom will not automatically retry timeout errors. Increase timeout or optimize request, then retry.


## Comparison: Bedrock vs Direct Anthropic

| Feature | Bedrock | Direct Anthropic |
|---------|---------|------------------|
| **Pricing** | Varies by region ($3-$15/1M) | Fixed global pricing ($3-$15/1M) |
| **Authentication** | AWS IAM (roles, keys) | API key |
| **Network** | VPC/PrivateLink support | Public internet only |
| **Compliance** | AWS compliance (HIPAA, SOC 2) | Anthropic compliance |
| **Latency** | Regional (5-180ms) | Global (varies) |
| **Features** | May lag behind | Latest features first |
| **Billing** | Consolidated AWS bill | Separate Anthropic invoice |
| **Setup Complexity** | Moderate (IAM, model access) | Simple (API key only) |
| **Multi-region** | AWS regions | Single global endpoint |
| **Tool Calling** | Native support | Native support |
| **Streaming** | Supported | Supported |

**Choose Bedrock for**:
- Enterprise AWS integration
- VPC isolation required
- Unified AWS billing preferred
- AWS compliance needs (HIPAA, SOC 2)
- Multi-region deployment

**Choose Direct Anthropic for**:
- Latest features first
- Simpler setup (no AWS account)
- Non-AWS environments
- Global deployment
- Single invoice preferred


## See Also

### Loom Documentation
- [LLM Providers Overview](./llm-providers/) - All supported LLM providers
- [Anthropic Direct](./llm-anthropic/) - Direct Anthropic integration
- [Agent Configuration](./agent-configuration/) - Agent YAML configuration
- [CLI Reference](./cli/) - Command-line interface

### AWS Documentation
- [AWS Bedrock Documentation](https://docs.aws.amazon.com/bedrock/) - Official Bedrock docs
- [Bedrock Pricing](https://aws.amazon.com/bedrock/pricing/) - Regional pricing
- [Bedrock Best Practices](https://docs.aws.amazon.com/bedrock/latest/userguide/best-practices.html) - AWS recommendations
- [Model Access](https://docs.aws.amazon.com/bedrock/latest/userguide/model-access.html) - Requesting model access
- [IAM Permissions](https://docs.aws.amazon.com/bedrock/latest/userguide/security-iam.html) - IAM policy examples

### Monitoring & Security
- [CloudWatch Metrics](https://docs.aws.amazon.com/bedrock/latest/userguide/monitoring-cloudwatch.html) - Bedrock metrics
- [CloudTrail Logging](https://docs.aws.amazon.com/bedrock/latest/userguide/logging-using-cloudtrail.html) - Audit logging
- [VPC Endpoints](https://docs.aws.amazon.com/bedrock/latest/userguide/vpc-interface-endpoints.html) - PrivateLink setup
- [Cost Alerts](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/monitor_estimated_charges_with_cloudwatch.html) - Cost monitoring

### Support
- [AWS Support](https://console.aws.amazon.com/support/) - AWS technical support
- [Loom Issues](https://github.com/teradata-labs/loom/issues) - Report Loom bugs
