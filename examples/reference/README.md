# Configuration Examples

YAML-based configuration for threads, backends, patterns, and workflows. Supports declarative, configuration-driven development.

## Directories

### agents/
Complete agent definitions ready to load and run.

**Contents:**
- Validated agent configs
- Domain-specific agent definitions
- Agent behavior specifications
- `agent-all-fields-reference.yaml` - Comprehensive reference showing all possible YAML fields

**Use case:** Configuration-driven agent deployment, version-controlled agent definitions.

### workflows/
Multi-step workflow definitions.

**Contents:**
- Sequential workflows
- Conditional branching
- Error handling patterns
- Retry strategies
- `workflow-all-fields-reference.yaml` - Comprehensive reference showing all possible workflow YAML fields for both orchestration patterns and event-driven workflows

**Use case:** Complex multi-step operations, orchestration patterns.

### agent-templates/
Reusable agent configuration templates.

**Contents:**
- Base agent configurations
- Common thread patterns
- Template variables for customization

**Use case:** Share agent configurations across projects, standardize agent setup.

---

**Configuration-driven development makes agents reproducible, testable, and maintainable!**
