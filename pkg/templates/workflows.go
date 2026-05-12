// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package templates

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// workflowTemplateRegistry is the authoritative list of workflow templates
// shipped with the OSS binary. Each entry bundles N agent specs (each
// referencing an AgentPreset for baseline config + a curated system prompt)
// and a fully-realized WorkflowPattern with stage prompts pre-filled. Agent
// ids inside the pattern are zero-valued in the template — the server's
// CreateWorkflowFromTemplate handler slots them in when the template is
// instantiated.
//
// Six templates mirror the cloud-side set, with stage prompts and agent
// roles preserved. The OSS port replaces the cloud-specific
// `create_ui_app` references where they overlap with the OSS catalog (it
// does — both have the same tool name) and uses the OSS WorkflowPattern
// oneof shape instead of the cloud's PatternConfig oneof.
var workflowTemplateRegistry = []*loomv1.WorkflowTemplateInfo{
	// 1. Research & Report Generator — Pipeline
	{
		Template:    loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_RESEARCH_REPORT,
		DisplayName: "Research & Report Generator",
		Description: "Deep research on any topic, synthesized into a polished report with an interactive dashboard.",
		Icon:        "file-search",
		Category:    "research",
		DefaultWorkflowPattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					PassFullHistory: true,
					Stages: []*loomv1.PipelineStage{
						{PromptTemplate: "Research this topic thoroughly. Find data, statistics, expert opinions, and primary sources. Organize your findings with clear citations.\n\nTopic: {{input}}"},
						{PromptTemplate: "Synthesize the following research into a clear, well-structured report. Include an executive summary, key findings, detailed analysis, and actionable recommendations.\n\nResearch:\n{{previous}}"},
						{PromptTemplate: "Use the create_ui_app tool to build an interactive HTML dashboard visualizing the key data points and findings from this report. Use charts (Chart.js), stat cards, and tables. Output a complete self-contained HTML document.\n\nReport:\n{{previous}}"},
					},
				},
			},
		},
		Agents: []*loomv1.WorkflowTemplateAgentSpec{
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName: "research-report:researcher",
				Description: "Deep web researcher for the Research & Report workflow.",
				SystemPrompt: `Conduct thorough, systematic research on the given topic.

- Search multiple sources for comprehensive coverage
- Prioritize recent, authoritative sources (academic papers, official reports, industry analyses)
- Include specific data points, statistics, and quotes with attribution
- Identify conflicting viewpoints and note the evidence for each
- Organize findings into clear themes or categories
- Flag any gaps in available information`,
			},
			{
				Preset:              loomv1.AgentPreset_AGENT_PRESET_CREATIVE_WRITER,
				DefaultName:         "research-report:writer",
				Description:         "Report writer for the Research & Report workflow.",
				TemperatureOverride: 0.7, // tighten the writer's default 1.0 — structured output, not freeform
				SystemPrompt: `Transform the provided research into a polished, executive-ready report. Your input ALWAYS contains research data provided by the previous pipeline stage. Do NOT search the web or conduct your own research. Use ONLY the research content provided in the user message.

Structure:
1. **Executive Summary** — 2-3 paragraph overview of key findings and recommendations
2. **Key Findings** — Numbered list of the most important discoveries
3. **Detailed Analysis** — Deep dive into each finding with supporting evidence
4. **Recommendations** — Actionable next steps based on the analysis
5. **Sources** — Organized list of all sources referenced

Write in a clear, professional tone. Use data to support every claim. All data and citations must come from the research provided to you — do not fabricate or search for additional sources.`,
			},
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_UI_SPECIALIST,
				DefaultName: "research-report:dashboard",
				Description: "Dashboard builder for the Research & Report workflow.",
				SystemPrompt: `You MUST use the create_ui_app tool to build an interactive HTML dashboard. Do not just describe what a dashboard would look like — actually create it.

Build a dashboard presenting the report's key findings visually:
- Extract quantitative data from the report for charts and stat cards
- Use appropriate chart types (bar for comparisons, line for trends, pie for composition)
- Create a stat-card row at the top with 3-4 headline metrics
- Include a summary table for detailed breakdowns
- Use clear titles and labels on every visualization
- Make the dashboard self-explanatory — someone should understand the findings without reading the full report

Use the create_ui_app tool with a complete, self-contained HTML document including inline CSS and JavaScript (Chart.js via CDN for charts).`,
			},
		},
	},

	// 2. Data-to-Dashboard — Pipeline
	{
		Template:    loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DATA_TO_DASHBOARD,
		DisplayName: "Data-to-Dashboard",
		Description: "Ask a data question in plain English — get SQL analysis and an interactive dashboard.",
		Icon:        "bar-chart-3",
		Category:    "data",
		DefaultWorkflowPattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					PassFullHistory: true,
					Stages: []*loomv1.PipelineStage{
						{PromptTemplate: "Analyze this data question. Write and execute the appropriate SQL queries. Interpret the results and summarize your findings with the actual data.\n\nQuestion: {{input}}"},
						{PromptTemplate: "Use the create_ui_app tool to build an interactive HTML dashboard visualizing the query results and analysis below. Use stat cards for headline numbers, charts (Chart.js) for trends and comparisons, and tables for detailed breakdowns. Output a complete self-contained HTML document.\n\nAnalysis:\n{{previous}}"},
					},
				},
			},
		},
		Agents: []*loomv1.WorkflowTemplateAgentSpec{
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_TERADATA_ANALYST,
				DefaultName: "data-dashboard:analyst",
				Description: "Data analyst for the Data-to-Dashboard workflow.",
				SystemPrompt: `Answer data questions by writing and executing SQL queries.

- Start by exploring the available tables and their schemas
- Write clear, well-commented SQL queries
- Execute queries and include the actual results
- Interpret the numbers — don't just show raw data
- Highlight trends, anomalies, and key takeaways
- If the question is ambiguous, make reasonable assumptions and state them
- Include both summary statistics and detailed breakdowns`,
			},
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_UI_SPECIALIST,
				DefaultName: "data-dashboard:visualizer",
				Description: "Dashboard builder for the Data-to-Dashboard workflow.",
				SystemPrompt: `You MUST use the create_ui_app tool to build an interactive HTML dashboard. Do not just describe what a dashboard would look like — actually create it.

Turn data analysis results into an interactive dashboard:
- Extract all quantitative data from the analysis for visualization
- Lead with 3-4 stat cards showing headline metrics
- Use bar charts for comparisons, line charts for time series, pie charts for proportions
- Include a detailed data table with the full query results
- Add clear titles, axis labels, and legends
- The dashboard should tell the story of the data at a glance

Use the create_ui_app tool with a complete, self-contained HTML document including inline CSS and JavaScript (Chart.js via CDN for charts).`,
			},
		},
	},

	// 3. Competitive Intelligence Brief — Fork-Join (SUMMARY merge)
	{
		Template:    loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_COMPETITIVE_INTEL,
		DisplayName: "Competitive Intelligence Brief",
		Description: "Three analysts research a target from different angles in parallel, merged into a SWOT brief.",
		Icon:        "target",
		Category:    "research",
		DefaultWorkflowPattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_ForkJoin{
				ForkJoin: &loomv1.ForkJoinPattern{
					Prompt:         "Research the target company along your angle and produce a structured brief. Target: {{input}}",
					MergeStrategy:  loomv1.MergeStrategy_SUMMARY,
					TimeoutSeconds: 1800,
				},
			},
		},
		Agents: []*loomv1.WorkflowTemplateAgentSpec{
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName: "competitive-intel:product-analyst",
				Description: "Product & market positioning analyst.",
				SystemPrompt: `Research the target company's products, services, pricing, and market positioning.

Focus areas:
- Product portfolio — what do they sell, to whom?
- Pricing strategy — how do they price vs. competitors?
- Market share and positioning — where do they sit in the market?
- Customer reviews and sentiment — what do customers say?
- Recent product launches or deprecations
- Go-to-market strategy and distribution channels`,
			},
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName: "competitive-intel:tech-analyst",
				Description: "Technology & innovation analyst.",
				SystemPrompt: `Research the target company's technology stack, patents, and engineering capabilities.

Focus areas:
- Technology stack — what platforms, languages, and infrastructure do they use?
- Patents and IP — what have they patented recently?
- Engineering culture — how do they hire, what do they value?
- Open source contributions and developer community
- Technical differentiators vs. competitors
- R&D investment and innovation pipeline`,
			},
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName: "competitive-intel:market-analyst",
				Description: "Financial & market analyst.",
				SystemPrompt: `Research the target company's financials, partnerships, and market dynamics.

Focus areas:
- Financial performance — revenue, growth, profitability (if public)
- Funding and investment — recent rounds, investors, valuation (if private)
- Strategic partnerships and acquisitions
- Recent news, press releases, and executive changes
- Market trends affecting the company
- Regulatory environment and compliance posture`,
			},
		},
	},

	// 4. Data Quality Audit — Pipeline (schedulable weekly)
	{
		Template:          loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DATA_QUALITY_AUDIT,
		DisplayName:       "Data Quality Audit",
		Description:       "Run a periodic data quality audit: anomaly detection on key tables, root-cause analysis, dashboard summary.",
		Icon:              "shield-check",
		Category:          "operations",
		Schedulable:       true,
		SuggestedCron:     "0 9 * * 1", // Monday 09:00
		SuggestedTimezone: "UTC",
		DefaultWorkflowPattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					PassFullHistory: true,
					Stages: []*loomv1.PipelineStage{
						{PromptTemplate: "Audit the data quality of the configured tables. Look for null spikes, schema drift, row-count anomalies, and unusual value distributions versus the last 7 days. Report findings with concrete numbers and table/column references.\n\nScope: {{input}}"},
						{PromptTemplate: "Given these data quality findings, identify the most likely root causes (upstream pipeline issues, schema changes, integration regressions). Prioritize findings by impact and propose specific investigation steps.\n\nFindings:\n{{previous}}"},
						{PromptTemplate: "Use the create_ui_app tool to build a data quality dashboard. Stat cards for headline counts (tables audited, issues found, P0 issues). A severity-coded table of findings with root-cause analysis. A trend chart if multiple audits have run.\n\nAnalysis:\n{{previous}}"},
					},
				},
			},
		},
		Agents: []*loomv1.WorkflowTemplateAgentSpec{
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_TERADATA_ANALYST,
				DefaultName: "dq-audit:analyst",
				Description: "Data quality analyst — runs anomaly queries on configured tables.",
				SystemPrompt: `Audit data quality for the configured tables. For each table:
- COUNT(*) versus the last 7 days
- NULL percentage per column
- Distinct-value count per categorical column
- Min/max for numeric columns
Flag anything that deviates more than 2× from recent history. Always cite the exact query and result.`,
			},
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName: "dq-audit:investigator",
				Description: "Root-cause investigator — correlates findings with upstream changes.",
				SystemPrompt: `Given a set of data quality findings, determine the likely root cause for each:
- Recent deployments or schema migrations
- Upstream pipeline failures or retries
- Integration partner outages
- Source-system batch timing shifts
Group findings by suspected cause. Propose specific investigation steps with owners where possible.`,
			},
			{
				Preset:      loomv1.AgentPreset_AGENT_PRESET_UI_SPECIALIST,
				DefaultName: "dq-audit:dashboard",
				Description: "Data quality dashboard builder.",
				SystemPrompt: `Build a Data Quality Audit dashboard via create_ui_app. Mandatory sections:
- Stat row: tables audited, total findings, P0 findings, P1 findings
- Severity-coded findings table (P0/P1/P2) with root-cause column
- Per-table issue counts as a bar chart
- A "since last audit" trend line if the data is available
Self-contained HTML + Chart.js (CDN).`,
			},
		},
	},

	// 5. Scheduled Performance Report — Pipeline (schedulable daily)
	{
		Template:          loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_PERFORMANCE_REPORT,
		DisplayName:       "Scheduled Performance Report",
		Description:       "Daily performance snapshot: metrics aggregation, narrative insight, dashboard delivery.",
		Icon:              "trending-up",
		Category:          "operations",
		Schedulable:       true,
		SuggestedCron:     "0 9 * * *", // every day 09:00
		SuggestedTimezone: "UTC",
		DefaultWorkflowPattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					PassFullHistory: true,
					Stages: []*loomv1.PipelineStage{
						{PromptTemplate: "Pull yesterday's key business metrics for the configured scope. Compute deltas versus the prior day, prior week, and prior 30-day average.\n\nScope: {{input}}"},
						{PromptTemplate: "Given these metric snapshots, write a concise narrative for executives: what changed, why (best hypothesis), and what to watch tomorrow.\n\nMetrics:\n{{previous}}"},
						{PromptTemplate: "Build a daily performance dashboard via create_ui_app. Hero stat row, period-over-period delta chart, narrative section, and a watch-list table for tomorrow.\n\nNarrative + metrics:\n{{previous}}"},
					},
				},
			},
		},
		Agents: []*loomv1.WorkflowTemplateAgentSpec{
			{
				Preset:       loomv1.AgentPreset_AGENT_PRESET_TERADATA_ANALYST,
				DefaultName:  "perf-report:analyst",
				Description:  "Metrics analyst.",
				SystemPrompt: `Pull the configured KPIs for the previous day and the relevant historical comparisons (day-over-day, week-over-week, 30-day average). Output a compact metrics block with absolute values, deltas, and trend direction.`,
			},
			{
				Preset:              loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName:         "perf-report:narrator",
				Description:         "Narrative writer for performance trends.",
				TemperatureOverride: 0.4,
				SystemPrompt: `Translate a metrics block into a 4-6 paragraph executive narrative:
1. Headline: what's the most important change today?
2. Drivers: what's behind the movement?
3. Risks: what watch-list items should the team monitor tomorrow?
4. Asks: what decisions or data does the team need to escalate?
No fluff. Cite exact numbers from the metrics block.`,
			},
			{
				Preset:       loomv1.AgentPreset_AGENT_PRESET_UI_SPECIALIST,
				DefaultName:  "perf-report:dashboard",
				Description:  "Performance dashboard builder.",
				SystemPrompt: `Daily performance dashboard via create_ui_app. Hero stat row (3 KPIs with deltas), main trend chart, narrative section, watch-list table. Self-contained HTML + Chart.js.`,
			},
		},
	},

	// 6. Deep Research Synthesis — Fork-Join (SUMMARY merge)
	{
		Template:    loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DEEP_RESEARCH,
		DisplayName: "Deep Research Synthesis",
		Description: "Three researchers tackle a question from technical, financial, and strategic angles, merged into a synthesis.",
		Icon:        "layers",
		Category:    "research",
		DefaultWorkflowPattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_ForkJoin{
				ForkJoin: &loomv1.ForkJoinPattern{
					Prompt:         "Research this question from your assigned angle. Produce a structured, well-sourced brief. Question: {{input}}",
					MergeStrategy:  loomv1.MergeStrategy_SUMMARY,
					TimeoutSeconds: 1800,
				},
			},
		},
		Agents: []*loomv1.WorkflowTemplateAgentSpec{
			{
				Preset:       loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName:  "deep-research:technical",
				Description:  "Technical-angle researcher.",
				SystemPrompt: `Investigate the question from a technical angle: implementation feasibility, engineering trade-offs, prior art, current best practices, and known failure modes. Cite primary sources where possible.`,
			},
			{
				Preset:       loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName:  "deep-research:financial",
				Description:  "Financial-angle researcher.",
				SystemPrompt: `Investigate the question from a financial angle: cost models, ROI estimates, capex vs. opex profile, comparable case studies with quantified outcomes, and market sizing where applicable.`,
			},
			{
				Preset:       loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName:  "deep-research:strategic",
				Description:  "Strategic-angle researcher.",
				SystemPrompt: `Investigate the question from a strategic angle: competitive positioning, opportunity cost, second-order effects, stakeholder alignment, and reversibility of the decision.`,
			},
		},
	},

	// 7. Skill Health Audit — Pipeline (schedulable weekly)
	{
		Template:          loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_SKILL_HEALTH_AUDIT,
		DisplayName:       "Skill Health Audit",
		Description:       "Periodic audit of skill library health: staleness detection, confidence decay, and deprecation recommendations.",
		Icon:              "heart-pulse",
		Category:          "operations",
		Schedulable:       true,
		SuggestedCron:     "0 9 * * 1",
		SuggestedTimezone: "UTC",
		DefaultWorkflowPattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					PassFullHistory: true,
					Stages: []*loomv1.PipelineStage{
						{PromptTemplate: "Audit all skill YAML files in the skills directory. For each skill, parse metadata.confidence, metadata.last_validated_ms, and metadata.status. Compute effective confidence using decay rate 0.995/day from last_validated_ms. Flag any skill where effective confidence < 0.7 OR days since validation > 90. Report: skill name, domain, confidence, days since validation, status, recommendation (refresh/deprecate/ok).\n\nScope: {{input}}"},
						{PromptTemplate: "Produce a concise skill health audit report from the findings. Group by domain. Highlight critical gaps (confidence < 0.5 or validated > 180 days). Suggest specific actions for each flagged skill (refresh content, re-validate, deprecate, or remove).\n\nFindings:\n{{previous}}"},
					},
				},
			},
		},
		Agents: []*loomv1.WorkflowTemplateAgentSpec{
			{
				Preset:       loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
				DefaultName:  "skill-audit:auditor",
				Description:  "Skill library auditor — scans all skills for staleness and confidence decay.",
				SystemPrompt: "Audit the skill library for health issues. For each skill YAML file:\n- Parse metadata.confidence and metadata.last_validated_ms\n- Compute effective confidence using decay rate 0.995/day from last_validated_ms to today\n- Flag skills where effective confidence < 0.7 OR days since validation > 90\n- Note skills with status: deprecated that are still referenced\n- Check for skills with risk_level: high/restricted lacking recent validation\n\nOutput a structured table: skill name | domain | confidence | days since validation | status | recommendation",
			},
			{
				Preset:              loomv1.AgentPreset_AGENT_PRESET_CREATIVE_WRITER,
				DefaultName:         "skill-audit:reporter",
				Description:         "Audit report writer — summarizes findings.",
				TemperatureOverride: 0.5,
				SystemPrompt:        "Produce a concise skill health audit report. Structure:\n1. Executive Summary — headline stats (total, healthy, flagged, critical)\n2. Critical Gaps — confidence < 0.5 or validated > 180 days\n3. Watch List — confidence 0.5-0.7 or validated 90-180 days\n4. By Domain — grouped findings with per-domain health score\n5. Recommended Actions — specific next steps per flagged skill\n\nUse only the data provided. Do not fabricate metrics.",
			},
		},
	},
}

// templateByEnum is a build-time index over workflowTemplateRegistry.
var templateByEnum = func() map[loomv1.WorkflowTemplate]*loomv1.WorkflowTemplateInfo {
	out := make(map[loomv1.WorkflowTemplate]*loomv1.WorkflowTemplateInfo, len(workflowTemplateRegistry))
	for _, t := range workflowTemplateRegistry {
		out[t.Template] = t
	}
	return out
}()

// ListWorkflowTemplates returns every registered template. The returned
// slice is the shared registry — callers must not mutate entries.
func ListWorkflowTemplates() []*loomv1.WorkflowTemplateInfo {
	return workflowTemplateRegistry
}

// GetWorkflowTemplate returns the template matching enum, or nil for
// UNSPECIFIED / unknown.
func GetWorkflowTemplate(t loomv1.WorkflowTemplate) *loomv1.WorkflowTemplateInfo {
	return templateByEnum[t]
}

// WorkflowTemplateEnumFromString resolves a kebab-case template name to
// its enum. Returns UNSPECIFIED for unknown strings.
func WorkflowTemplateEnumFromString(s string) loomv1.WorkflowTemplate {
	switch s {
	case "research-report":
		return loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_RESEARCH_REPORT
	case "data-to-dashboard":
		return loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DATA_TO_DASHBOARD
	case "competitive-intel":
		return loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_COMPETITIVE_INTEL
	case "data-quality-audit":
		return loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DATA_QUALITY_AUDIT
	case "performance-report":
		return loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_PERFORMANCE_REPORT
	case "deep-research":
		return loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DEEP_RESEARCH
	case "skill-health-audit":
		return loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_SKILL_HEALTH_AUDIT
	default:
		return loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_UNSPECIFIED
	}
}

// WorkflowTemplateEnumToString is the inverse of the From variant. Returns
// empty string for UNSPECIFIED so callers can detect a missing template.
func WorkflowTemplateEnumToString(t loomv1.WorkflowTemplate) string {
	switch t {
	case loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_RESEARCH_REPORT:
		return "research-report"
	case loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DATA_TO_DASHBOARD:
		return "data-to-dashboard"
	case loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_COMPETITIVE_INTEL:
		return "competitive-intel"
	case loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DATA_QUALITY_AUDIT:
		return "data-quality-audit"
	case loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_PERFORMANCE_REPORT:
		return "performance-report"
	case loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DEEP_RESEARCH:
		return "deep-research"
	case loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_SKILL_HEALTH_AUDIT:
		return "skill-health-audit"
	default:
		return ""
	}
}
