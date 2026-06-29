// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// runtime.js - Component runtime for dynamically compiled MCP apps.
//
// Reads a spec from <script type="application/json" id="app-spec"> and renders
// components into <div id="app-root">.  Embedded into every dynamically
// compiled HTML app by the Go compiler.
//
// Security invariants:
//   - Object.freeze(APP_SPEC) after parse
//   - No innerHTML anywhere; all DOM via createElement/textContent/setAttribute
//   - setSafeAttribute rejects on* and dangerous href values
//   - SVG rendering uses strict element/attribute allowlists
//   - Chart.js config constructed from allowlisted fields only

(function () {
  "use strict";

  // ---------------------------------------------------------------------------
  // 1. Parse and freeze the app spec
  // ---------------------------------------------------------------------------

  const specElement = document.getElementById("app-spec");
  if (!specElement) {
    console.error("runtime.js: missing <script id='app-spec'> data block");
    return;
  }

  let APP_SPEC;
  try {
    APP_SPEC = JSON.parse(specElement.textContent);
  } catch (parseErr) {
    console.error("runtime.js: failed to parse app-spec JSON:", parseErr);
    return;
  }
  Object.freeze(APP_SPEC);

  const APP_ROOT = document.getElementById("app-root");
  if (!APP_ROOT) {
    console.error("runtime.js: missing <div id='app-root'>");
    return;
  }

  // ---------------------------------------------------------------------------
  // 2. Theme: Teradata design tokens (dark + light)
  // ---------------------------------------------------------------------------

  const THEMES = {
    dark: {
      bg: "#000712",
      surface: "#182032",
      card: "#1e273c",
      elevated: "#26547c",
      hover: "#263a5c",
      border: "#2a3a5c",
      textPrimary: "#e8eaf6",
      textSecondary: "#8892b0",
      textMuted: "#4a5580",
      // Semantic colors — lighter shades for readability on dark backgrounds.
      accent: "#688bf1",
      success: "#68a366",
      warning: "#f7ac4d",
      error: "#e0938e",
      cyan: "#60a9ed",
      magenta: "#c84d7b",
    },
    light: {
      bg: "#f8f9fb",
      surface: "#ffffff",
      card: "#eff1f7",
      elevated: "#e3e6f0",
      hover: "#dde1f0",
      border: "#d0d5e8",
      textPrimary: "#1a1a2e",
      textSecondary: "#555577",
      textMuted: "#9090aa",
      // Semantic colors — darker shades for readability on light backgrounds.
      accent: "#2952cc",
      success: "#1e6b2e",
      warning: "#8b3900",
      error: "#b01010",
      cyan: "#0055bb",
      magenta: "#7b2f8e",
    },
  };

  // Mutable theme — starts on the host-injected theme (falls back to dark).
  // Includes both structural tokens (bg, surface, …) and semantic colors (accent, …).
  const _initialThemeName =
    window.__TERA_INITIAL_THEME__ && THEMES[window.__TERA_INITIAL_THEME__]
      ? window.__TERA_INITIAL_THEME__
      : "dark";
  let THEME = {
    ...THEMES[_initialThemeName],
    fontSans: "'Inter', Arial, sans-serif",
    fontMono: "'Roboto Mono', monospace",
  };

  // Render-generation counter: incremented on every fullRender() call so that
  // stale async renders (from a previous generation) can detect they are
  // superseded and abort before appending to the DOM.
  let renderGen = 0;

  // Semantic color names that can be used in component specs (e.g. color: "accent").
  const SEMANTIC_COLOR_KEYS = new Set([
    "accent",
    "success",
    "warning",
    "error",
    "cyan",
    "magenta",
  ]);

  // Chart types that render as slices (no x/y axes, one color per slice).
  const SLICE_CHART_TYPES = new Set(["pie", "doughnut", "polarArea"]);

  // Chart palette — vibrant colors that read well on dark backgrounds.
  const CHART_PALETTE = [
    "#688bf1",
    "#52c07d",
    "#f7ac4d",
    "#c84d7b",
    "#60a9ed",
    "#e0938e",
    "#a78bfa",
    "#34d399",
    "#fbbf24",
    "#fb7185",
    "#38bdf8",
    "#86efac",
  ];

  // Map common LLM-generated color names to theme-aware semantic colors.
  const COLOR_NAME_ALIASES = {
    green: "success", blue: "accent", red: "error",
    yellow: "warning", orange: "warning", purple: "magenta",
    pink: "magenta", teal: "cyan", gray: "textMuted", grey: "textMuted",
    white: "#ffffff", black: "#000000",
  };

  function resolveColor(color, fallback) {
    // Pass arrays through so per-slice backgroundColor arrays are preserved.
    if (Array.isArray(color))
      return color.map((c) => resolveColor(c, fallback));
    if (!color) return fallback || THEME.accent;
    // Resolve named semantic tokens from the active theme so colors adapt to dark/light.
    if (SEMANTIC_COLOR_KEYS.has(color)) return THEME[color];
    if (/^#[0-9a-fA-F]{3,8}$/.test(color)) return color;
    // Accept rgb/rgba/hsl/hsla values passed directly from LLM-generated specs.
    if (/^rgba?\(|^hsla?\(/.test(color)) return color;
    // Map common CSS color name aliases to theme-aware values.
    const alias = COLOR_NAME_ALIASES[color.toLowerCase()];
    if (alias) return resolveColor(alias, fallback);
    return fallback || THEME.accent;
  }

  // ---------------------------------------------------------------------------
  // 3. Safe DOM helpers
  // ---------------------------------------------------------------------------

  // Attribute names starting with 'on' (event handlers) are blocked.
  // href and xlink:href are blocked to prevent javascript: URIs.
  const BLOCKED_ATTR_PATTERN = /^on/i;
  const BLOCKED_ATTR_NAMES = new Set(["href", "xlink:href"]);

  function setSafeAttribute(el, name, value) {
    if (typeof name !== "string") return;
    const lower = name.toLowerCase();
    if (BLOCKED_ATTR_PATTERN.test(lower)) return;
    if (BLOCKED_ATTR_NAMES.has(lower)) return;
    el.setAttribute(name, value);
  }

  function createElement(tag, attrs, textContent) {
    const el = document.createElement(tag);
    if (attrs) {
      for (const key of Object.keys(attrs)) {
        if (!Object.hasOwn(attrs, key)) continue;
        const val = attrs[key];
        if (key === "style" && typeof val === "object") {
          for (const prop of Object.keys(val)) {
            if (Object.hasOwn(val, prop)) {
              el.style[prop] = val[prop];
            }
          }
        } else if (key === "className") {
          el.className = String(val);
        } else {
          setSafeAttribute(el, key, String(val));
        }
      }
    }
    if (textContent !== undefined && textContent !== null) {
      el.textContent = String(textContent);
    }
    return el;
  }

  // ---------------------------------------------------------------------------
  // 4. SVG helpers with strict allowlists
  // ---------------------------------------------------------------------------

  const SVG_NS = "http://www.w3.org/2000/svg";

  const SVG_ALLOWED_ELEMENTS = new Set([
    "svg",
    "g",
    "rect",
    "circle",
    "line",
    "path",
    "text",
    "tspan",
    "defs",
    "marker",
    "polygon",
    "polyline",
  ]);

  // Broad but safe SVG attribute allowlist
  const SVG_ALLOWED_ATTRS = new Set([
    "viewBox",
    "width",
    "height",
    "x",
    "y",
    "x1",
    "y1",
    "x2",
    "y2",
    "cx",
    "cy",
    "r",
    "rx",
    "ry",
    "d",
    "points",
    "fill",
    "stroke",
    "stroke-width",
    "stroke-dasharray",
    "stroke-linecap",
    "stroke-linejoin",
    "opacity",
    "transform",
    "text-anchor",
    "font-family",
    "font-size",
    "font-weight",
    "dominant-baseline",
    "marker-end",
    "marker-start",
    "markerWidth",
    "markerHeight",
    "refX",
    "refY",
    "orient",
    "id",
    "class",
  ]);

  function createSVGElement(tag, attrs) {
    const safeName = String(tag).toLowerCase();
    if (!SVG_ALLOWED_ELEMENTS.has(safeName)) {
      console.warn("runtime.js: blocked SVG element:", safeName);
      return null;
    }
    const el = document.createElementNS(SVG_NS, safeName);
    if (attrs) {
      for (const key of Object.keys(attrs)) {
        if (!Object.hasOwn(attrs, key)) continue;
        if (!SVG_ALLOWED_ATTRS.has(key)) continue;
        el.setAttribute(key, String(attrs[key]));
      }
    }
    return el;
  }

  function createSVGText(tag, attrs, text) {
    const el = createSVGElement(tag, attrs);
    if (el && text !== undefined && text !== null) {
      el.textContent = String(text);
    }
    return el;
  }

  // ---------------------------------------------------------------------------
  // 5. Layout engine
  // ---------------------------------------------------------------------------

  function applyLayout(container, layout) {
    if (!layout) return;

    // The spec layout field is a string ('stack', 'grid-2', 'grid-3', 'grid').
    // Also support object form {type, gap, columns, direction} for future use.
    var type, gap, direction, columns;
    if (typeof layout === "string") {
      type = layout;
      gap = "24px";
    } else if (typeof layout === "object") {
      type = layout.type || "stack";
      gap = layout.gap || "24px";
      direction = layout.direction;
      columns = layout.columns;
    } else {
      return;
    }

    if (type === "grid-2") {
      container.style.display = "grid";
      container.style.gap = gap;
      // auto-fit + minmax collapses to 1 column on narrow viewports automatically
      container.style.gridTemplateColumns =
        "repeat(auto-fit, minmax(min(100%, 320px), 1fr))";
    } else if (type === "grid-3") {
      container.style.display = "grid";
      container.style.gap = gap;
      container.style.gridTemplateColumns =
        "repeat(auto-fit, minmax(min(100%, 250px), 1fr))";
    } else if (type === "grid" || type === "grid-4") {
      container.style.display = "grid";
      container.style.gap = gap;
      if (columns) {
        container.style.gridTemplateColumns =
          "repeat(" + Math.max(1, Number(columns) || 1) + ", 1fr)";
      } else {
        // Responsive 2→3→4 columns: medium ~704px → 2 cols, large ~1068px → 3 cols,
        // XL ~1432px → 4 cols. calc(25% - 18px) caps at exactly 4 cols on wide screens.
        container.style.gridTemplateColumns =
          "repeat(auto-fit, minmax(min(100%, max(340px, calc(25% - 18px))), 1fr))";
      }
    } else {
      // stack (default) = vertical flex
      container.style.display = "flex";
      container.style.flexDirection =
        direction === "horizontal" ? "row" : "column";
      container.style.gap = gap;
      if (direction === "horizontal") {
        container.style.flexWrap = "wrap";
      }
    }
  }

  // ---------------------------------------------------------------------------
  // 6. Chart.js lazy loading
  // ---------------------------------------------------------------------------

  let chartJSPromise = null;

  function ensureChartJS() {
    if (window.Chart) return Promise.resolve();
    if (chartJSPromise) return chartJSPromise;
    chartJSPromise = new Promise((resolve, reject) => {
      const s = document.createElement("script");
      s.src =
        "https://cdn.jsdelivr.net/npm/chart.js@4.4.7/dist/chart.umd.min.js";
      s.integrity =
        "sha384-vsrfeLOOY6KuIYKDlmVH5UiBmgIdB1oEf7p01YgWHuqmOHfZr374+odEv96n9tNC";
      s.crossOrigin = "anonymous";
      s.onload = resolve;
      s.onerror = () => reject(new Error("Failed to load Chart.js"));
      document.head.appendChild(s);
    });
    return chartJSPromise;
  }

  // Build a safe Chart.js config from allowlisted fields only
  const CHART_ALLOWED_TYPES = new Set([
    "bar",
    "line",
    "pie",
    "doughnut",
    "radar",
    "polarArea",
    "scatter",
    "bubble",
  ]);

  const CHART_DATASET_COLOR_FIELDS = new Set([
    "backgroundColor",
    "borderColor",
    "pointBackgroundColor",
    "pointBorderColor",
    "hoverBackgroundColor",
    "hoverBorderColor",
  ]);

  // Returns true for values JS Number() coerces to a finite number (numbers and
  // numeric strings), which is what a chart can actually plot.
  function isNumericValue(v) {
    if (typeof v === "number") return isFinite(v);
    if (typeof v === "string" && v.trim() !== "") return isFinite(Number(v));
    return false;
  }

  // Infer an {xKey, series} mapping from an array of row objects when the spec
  // omits x_key/series. The most common LLM-produced chart shape is a plain
  // array of records (e.g. [{hour:"06:00", speed:24.5}, ...]); without a mapping
  // the old code coerced each row object to NaN and rendered an empty chart.
  // The first non-numeric field becomes the x-axis; every numeric field becomes
  // a series. Returns null when no usable numeric series can be found.
  function inferRecordsMapping(rows) {
    const first = rows[0];
    if (!first || typeof first !== "object" || Array.isArray(first)) return null;
    const keys = Object.keys(first);
    if (keys.length < 2) return null;
    let xKey = keys.find((k) => !isNumericValue(first[k]));
    if (xKey === undefined) xKey = keys[0]; // all numeric: use the first column as the axis
    const series = keys
      .filter((k) => k !== xKey && isNumericValue(first[k]))
      .map((k) => ({ key: k, label: k }));
    if (series.length === 0) return null;
    return { xKey, series };
  }

  // Reports whether a built Chart.js config has at least one finite value
  // (axis charts) or one valid {x,y} point (scatter/bubble) to plot.
  function chartConfigHasData(config) {
    const datasets =
      config && config.data && Array.isArray(config.data.datasets)
        ? config.data.datasets
        : [];
    for (const ds of datasets) {
      if (!ds || !Array.isArray(ds.data)) continue;
      for (const v of ds.data) {
        if (typeof v === "number" && isFinite(v)) return true;
        if (
          v &&
          typeof v === "object" &&
          typeof v.x === "number" &&
          isFinite(v.x) &&
          typeof v.y === "number" &&
          isFinite(v.y)
        ) {
          return true;
        }
      }
    }
    return false;
  }

  function buildSafeChartConfig(props) {
    // Support snake_case (chart_type), camelCase (chartType), and plain (type).
    const rawType = props.chartType || props.chart_type || props.type;
    const chartType = CHART_ALLOWED_TYPES.has(rawType) ? rawType : "bar";

    let labels = [];
    let datasets = [];

    // Records format: data=[{x_key_val, series1_val, ...}] + x_key + series=[{key, label}]
    // Also supports yKeys/labels/colors shorthand instead of series objects.
    const xKey = props.x_key || props.xKey;
    const yKeys = Array.isArray(props.yKeys) ? props.yKeys : Array.isArray(props.y_keys) ? props.y_keys : null;
    const seriesDef = Array.isArray(props.series)
      ? props.series
      : yKeys
        ? yKeys.map((key, i) => ({
            key,
            label: (Array.isArray(props.labels) && props.labels[i]) || key,
            color: (Array.isArray(props.colors) && props.colors[i]) || null,
          }))
        : null;

    // Detect a plain array of row objects, then fill in any missing x_key/series
    // by inference so the common mapping-less shape renders instead of NaN-ing.
    const dataIsRowObjects =
      Array.isArray(props.data) &&
      props.data.length > 0 &&
      props.data[0] !== null &&
      typeof props.data[0] === "object" &&
      !Array.isArray(props.data[0]);
    let effectiveXKey = xKey;
    let effectiveSeriesDef = seriesDef;
    if (dataIsRowObjects && (!effectiveXKey || !effectiveSeriesDef)) {
      const inferred = inferRecordsMapping(props.data);
      if (inferred) {
        if (!effectiveXKey) effectiveXKey = inferred.xKey;
        if (!effectiveSeriesDef) effectiveSeriesDef = inferred.series;
      }
    }
    const isRecordsFormat =
      effectiveXKey && effectiveSeriesDef && dataIsRowObjects;

    const isPointChart = chartType === "scatter" || chartType === "bubble";

    if (isRecordsFormat) {
      // Scatter/bubble charts don't use labels — x comes from the data points.
      if (!isPointChart) {
        labels = props.data.map((row) => String(row[effectiveXKey] ?? ""));
      }
      for (const s of effectiveSeriesDef) {
        if (!s || typeof s !== "object" || !s.key) continue;
        const rKey = props.r_key || props.rKey || s.r_key || s.rKey;
        const safeDS = {
          label: String(s.label || s.key),
          data: props.data.map((row) => {
            if (isPointChart) {
              const pt = { x: Number(row[effectiveXKey]), y: Number(row[s.key]) };
              if (chartType === "bubble") {
                pt.r = rKey && row[rKey] != null ? Number(row[rKey]) : 5;
              }
              return pt;
            }
            const v = row[s.key];
            return v !== null && v !== undefined ? Number(v) : null;
          }),
        };
        if (SLICE_CHART_TYPES.has(chartType)) {
          // Pie/doughnut/polarArea need one color per slice, not one per series.
          safeDS.backgroundColor = labels.map(
            (_, i) => CHART_PALETTE[i % CHART_PALETTE.length] + "cc",
          );
          safeDS.borderColor = labels.map(
            (_, i) => CHART_PALETTE[i % CHART_PALETTE.length],
          );
        } else {
          // Assign a distinct palette color per series so multi-series charts aren't monochrome.
          const c = s.color
            ? resolveColor(s.color)
            : CHART_PALETTE[datasets.length % CHART_PALETTE.length];
          safeDS.backgroundColor = c + "aa";
          safeDS.borderColor = c;
        }
        if (props.fill !== undefined) safeDS.fill = Boolean(props.fill);

        // Per-type border-width defaults for a clean modern look.
        if (chartType === "pie" || chartType === "doughnut") {
          safeDS.borderWidth = 0;
          delete safeDS.borderColor;
        } else if (chartType === "line" || chartType === "radar") {
          safeDS.borderWidth = 2;
        } else if (chartType === "polarArea") {
          safeDS.borderWidth = 1;
        }

        datasets.push(safeDS);
      }
    } else {
      // Support both flat (props.labels/datasets) and nested (props.data.labels/datasets) formats.
      let dataObj =
        props.data && typeof props.data === "object" ? props.data : props;

      // Normalize flat top-level array: props.data = [1,2,3]. Preserve any
      // sibling props.labels so a {labels, data} pair still gets its x-axis.
      if (Array.isArray(props.data)) {
        dataObj = {
          labels: Array.isArray(props.labels) ? props.labels : [],
          datasets: [{ data: props.data }],
        };
      }
      // Normalize missing datasets when a flat .data array is present inside the object
      if (!Array.isArray(dataObj.datasets) && Array.isArray(dataObj.data)) {
        dataObj = {
          labels: dataObj.labels || [],
          datasets: [{ data: dataObj.data, label: props.title || "" }],
        };
      }

      labels = Array.isArray(dataObj.labels) ? dataObj.labels.map(String) : [];

      const rawDatasets = Array.isArray(dataObj.datasets)
        ? dataObj.datasets
        : [];
      if (rawDatasets.length > 0) {
        for (const ds of rawDatasets) {
          if (!ds || typeof ds !== "object") continue;
          const safeDS = {
            // Scatter/bubble data is [{x,y}] or [{x,y,r}] — preserve objects as-is.
            data: Array.isArray(ds.data)
              ? isPointChart
                ? ds.data.filter((p) => p !== null && p !== undefined && typeof p === "object")
                : ds.data.map(Number)
              : [],
          };
          if (ds.label) safeDS.label = String(ds.label);

          // Color fields
          for (const field of CHART_DATASET_COLOR_FIELDS) {
            if (Object.hasOwn(ds, field)) {
              safeDS[field] = resolveColor(ds[field]);
            }
          }
          // Shorthand: ds.color -> backgroundColor + borderColor.
          // Fall back to palette so multi-dataset charts aren't monochrome.
          if (ds.color || !safeDS.backgroundColor) {
            const c = ds.color
              ? resolveColor(ds.color)
              : CHART_PALETTE[datasets.length % CHART_PALETTE.length];
            if (!safeDS.backgroundColor) safeDS.backgroundColor = c + "aa";
            if (!safeDS.borderColor) safeDS.borderColor = c;
          }
          if (props.fill !== undefined) safeDS.fill = Boolean(props.fill);

          // Pie/doughnut/polarArea need one color per slice, not one per dataset.
          if (SLICE_CHART_TYPES.has(chartType)) {
            if (!Array.isArray(safeDS.backgroundColor)) {
              safeDS.backgroundColor = safeDS.data.map(
                (_, i) => CHART_PALETTE[i % CHART_PALETTE.length] + "cc",
              );
            }
            if (!Array.isArray(safeDS.borderColor)) {
              safeDS.borderColor = safeDS.data.map(
                (_, i) => CHART_PALETTE[i % CHART_PALETTE.length],
              );
            }
          }

          // Per-type border-width defaults; spec value takes precedence for non-pie types.
          if (chartType === "pie" || chartType === "doughnut") {
            safeDS.borderWidth = 0;
            delete safeDS.borderColor;
          } else if (chartType === "line" || chartType === "radar") {
            safeDS.borderWidth =
              ds.borderWidth != null ? Number(ds.borderWidth) : 2;
            if (ds.tension != null) safeDS.tension = Number(ds.tension);
            if (ds.pointRadius != null)
              safeDS.pointRadius = Number(ds.pointRadius);
          } else if (chartType === "polarArea") {
            safeDS.borderWidth =
              ds.borderWidth != null ? Number(ds.borderWidth) : 1;
          }

          datasets.push(safeDS);
        }
      }
    }

    const config = {
      type: chartType,
      data: { labels, datasets },
      options: {
        responsive: true,
        maintainAspectRatio: SLICE_CHART_TYPES.has(chartType),
        layout: SLICE_CHART_TYPES.has(chartType) ? { padding: 12 } : { padding: 0 },
        plugins: {
          legend: {
            labels: {
              color: THEME.textSecondary,
              font: { family: THEME.fontMono, size: 11 },
            },
          },
          tooltip: {
            backgroundColor: THEME.surface,
            titleColor: THEME.textPrimary,
            bodyColor: THEME.textPrimary,
            borderColor: THEME.border,
            borderWidth: 1,
            bodyFont: { family: THEME.fontMono, size: 11 },
          },
        },
        scales: {},
      },
    };

    // Merge user-provided options (e.g., indexAxis for horizontal bars, scale hiding).
    const userOpts =
      props.data && typeof props.data === "object" && props.data.options
        ? props.data.options
        : props.options || null;
    if (userOpts) {
      if (userOpts.indexAxis) config.options.indexAxis = userOpts.indexAxis;
      if (userOpts.scales) {
        for (const [axis, axisOpts] of Object.entries(userOpts.scales)) {
          config.options.scales[axis] = Object.assign(
            config.options.scales[axis] || {},
            axisOpts,
          );
        }
      }
    }

    // Stacked mode
    if (props.stacked) {
      config.options.scales.x = {
        stacked: true,
        grid: { color: THEME.border + "80" },
        ticks: {
          color: THEME.textSecondary,
          font: { family: THEME.fontMono, size: 10 },
        },
      };
      config.options.scales.y = {
        stacked: true,
        grid: { color: THEME.border + "80" },
        ticks: {
          color: THEME.textSecondary,
          font: { family: THEME.fontMono, size: 10 },
        },
      };
    } else if (
      chartType === "bar" ||
      chartType === "line" ||
      chartType === "scatter" ||
      chartType === "bubble"
    ) {
      config.options.scales.x = {
        grid: { color: THEME.border + "80" },
        ticks: {
          color: THEME.textSecondary,
          font: { family: THEME.fontMono, size: 10 },
        },
      };
      config.options.scales.y = {
        grid: { color: THEME.border + "80" },
        ticks: {
          color: THEME.textSecondary,
          font: { family: THEME.fontMono, size: 10 },
        },
      };
    }

    return config;
  }

  // ---------------------------------------------------------------------------
  // 7. Component registry  (type -> render function)
  // ---------------------------------------------------------------------------

  // Every render function receives (props, children) and returns a DOM element
  // or null.  Async render functions (chart) return a Promise<Element>.

  const COMPONENT_REGISTRY = Object.freeze({
    // ---- Display ----
    "stat-cards": renderStatCards,
    chart: renderChart,
    table: renderTable,
    "key-value": renderKeyValue,
    text: renderText,
    "code-block": renderCodeBlock,
    "progress-bar": renderProgressBar,
    badges: renderBadges,
    heatmap: renderHeatmap,

    // ---- Layout ----
    header: renderHeader,
    section: renderSection,
    tabs: renderTabs,

    // ---- Complex ----
    dag: renderDAG,
    "message-list": renderMessageList,
  });

  // ---------------------------------------------------------------------------
  // 8. Component walker (iterative, max depth 10)
  // ---------------------------------------------------------------------------

  // Track all active Chart.js instances for cleanup
  const chartInstances = [];

  function destroyAllCharts() {
    for (const c of chartInstances) {
      try {
        c.destroy();
      } catch (_) {
        /* ignore */
      }
    }
    chartInstances.length = 0;
  }

  async function renderComponents(components, parentEl, depth, gen) {
    if (!Array.isArray(components)) return;
    if (depth === undefined) depth = 0;
    if (depth > 10) {
      const warn = createElement(
        "div",
        {
          style: {
            color: THEME.error,
            fontFamily: THEME.fontMono,
            fontSize: "12px",
            padding: "8px",
          },
        },
        "Max component nesting depth (10) exceeded",
      );
      parentEl.appendChild(warn);
      return;
    }

    for (const comp of components) {
      if (!comp || typeof comp !== "object") continue;
      const type = comp.type;
      const props = comp.props || {};
      const children = comp.children;
      const compId = comp.id;

      const renderFn = Object.hasOwn(COMPONENT_REGISTRY, type)
        ? COMPONENT_REGISTRY[type]
        : null;
      if (!renderFn) {
        const unknown = createElement(
          "div",
          {
            style: {
              color: THEME.warning,
              fontFamily: THEME.fontMono,
              fontSize: "12px",
              padding: "8px",
            },
          },
          "Unknown component type: " + type,
        );
        parentEl.appendChild(unknown);
        continue;
      }

      // Error boundary: wrap every component render
      try {
        const result = renderFn(props, children, depth, gen);
        // Handle async (chart)
        const el = result instanceof Promise ? await result : result;
        if (renderGen !== gen) return;
        if (el) {
          if (compId) setSafeAttribute(el, "data-component-id", String(compId));
          parentEl.appendChild(el);
        }
      } catch (renderErr) {
        if (renderGen !== gen) return;
        const errBox = createElement(
          "div",
          {
            style: {
              color: THEME.error,
              background: THEME.card,
              border: "1px solid " + THEME.error,
              borderRadius: "6px",
              padding: "12px",
              fontFamily: THEME.fontMono,
              fontSize: "12px",
              marginBottom: "8px",
            },
          },
          "Render error (" +
            type +
            "): " +
            String(renderErr.message || renderErr),
        );
        parentEl.appendChild(errBox);
      }
    }
  }

  // ---------------------------------------------------------------------------
  // 9. Component render functions
  // ---------------------------------------------------------------------------

  // --- stat-cards ---
  function renderStatCards(props) {
    const container = createElement("div", {
      style: { display: "flex", gap: "16px", flexWrap: "wrap" },
    });
    const items = Array.isArray(props.items)
      ? props.items
      : Array.isArray(props.cards)
        ? props.cards
        : [];
    for (const item of items) {
      if (!item || typeof item !== "object") continue;
      const card = createElement("div", {
        style: {
          background: THEME.surface,
          border: "1px solid " + THEME.border,
          borderRadius: "8px",
          padding: "16px",
          flex: "1",
          minWidth: "150px",
        },
      });
      const label = createElement(
        "div",
        {
          style: {
            fontSize: "11px",
            color: THEME.textSecondary,
            textTransform: "uppercase",
            letterSpacing: "0.5px",
            marginBottom: "4px",
          },
        },
        item.label,
      );
      card.appendChild(label);

      const value = createElement(
        "div",
        {
          style: {
            fontSize: "24px",
            fontWeight: "700",
            fontFamily: THEME.fontMono,
            color: resolveColor(item.color, THEME.accent),
          },
        },
        item.value,
      );
      card.appendChild(value);

      if (item.sublabel) {
        const sub = createElement(
          "div",
          {
            style: {
              fontSize: "11px",
              color: THEME.textMuted,
              marginTop: "4px",
              fontFamily: THEME.fontMono,
            },
          },
          item.sublabel,
        );
        card.appendChild(sub);
      }
      container.appendChild(card);
    }
    return container;
  }

  // --- chart ---
  async function renderChart(props, _children, _depth, gen) {
    const wrapper = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        padding: "20px",
      },
    });

    if (props.title) {
      const title = createElement(
        "div",
        {
          style: {
            fontSize: "14px",
            fontWeight: "600",
            marginBottom: "12px",
            color: THEME.textPrimary,
          },
        },
        props.title,
      );
      wrapper.appendChild(title);
    }

    const rawChartType = props.chartType || props.chart_type || props.type;
    const isSliceChart = SLICE_CHART_TYPES.has(rawChartType);
    const chartHeight = props.height
      ? String(props.height).match(/^\d+$/) ? props.height + "px" : String(props.height)
      : isSliceChart ? null : "260px";
    const canvasWrap = createElement("div", {
      style: chartHeight
        ? { position: "relative", height: chartHeight, maxHeight: chartHeight }
        // Slice charts maintain their aspect ratio — cap width so the canvas
        // doesn't grow to fill the full container and center it.
        : { position: "relative", maxWidth: "min(320px, 100%)", margin: "0 auto" },
    });
    const canvas = document.createElement("canvas");
    canvasWrap.appendChild(canvas);
    wrapper.appendChild(canvasWrap);

    try {
      await ensureChartJS();
      // Abort if a newer render generation has started while Chart.js was loading.
      if (gen !== undefined && renderGen !== gen) return null;
      const config = buildSafeChartConfig(props);
      // If normalization produced no finite data, show a clear empty-state
      // instead of a blank canvas with an "undefined" legend and a 0–1 axis.
      if (!chartConfigHasData(config)) {
        canvasWrap.remove();
        wrapper.appendChild(
          createElement(
            "div",
            {
              style: {
                color: THEME.textSecondary,
                fontFamily: THEME.fontMono,
                fontSize: "12px",
                padding: "16px 8px",
                textAlign: "center",
              },
            },
            "No plottable data for this chart. Provide labels + datasets with numeric data arrays.",
          ),
        );
        return wrapper;
      }
      const instance = new window.Chart(canvas.getContext("2d"), config);
      chartInstances.push(instance);
    } catch (chartErr) {
      const errMsg = createElement(
        "div",
        {
          style: {
            color: THEME.error,
            fontFamily: THEME.fontMono,
            fontSize: "12px",
            padding: "8px",
          },
        },
        "Chart error: " + String(chartErr.message || chartErr),
      );
      wrapper.appendChild(errMsg);
    }

    return wrapper;
  }

  // --- table ---
  function renderTable(props) {
    const wrapper = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        padding: "20px",
        overflowX: "auto",
      },
    });

    if (props.title) {
      wrapper.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "14px",
              fontWeight: "600",
              marginBottom: "12px",
              color: THEME.textPrimary,
            },
          },
          props.title,
        ),
      );
    }

    if (props.maxHeight) {
      wrapper.style.maxHeight = props.maxHeight;
      wrapper.style.overflowY = "auto";
    }

    const columns = Array.isArray(props.columns) ? props.columns : [];
    const rows = Array.isArray(props.rows) ? props.rows : [];

    const table = createElement("table", {
      style: {
        width: "100%",
        minWidth: "480px",
        borderCollapse: "collapse",
        fontFamily: THEME.fontMono,
        fontSize: "12px",
      },
    });

    // Header
    const thead = document.createElement("thead");
    const headerRow = document.createElement("tr");
    for (const col of columns) {
      const th = createElement(
        "th",
        {
          style: {
            textAlign: "left",
            padding: "8px 12px",
            borderBottom: "2px solid " + THEME.border,
            color: THEME.textSecondary,
            fontSize: "11px",
            textTransform: "uppercase",
            letterSpacing: "0.5px",
            whiteSpace: "nowrap",
            cursor: props.sortable ? "pointer" : "default",
          },
        },
        typeof col === "string" ? col : col.label || col.key || "",
      );
      headerRow.appendChild(th);
    }
    thead.appendChild(headerRow);
    table.appendChild(thead);

    // Build column keys for data extraction
    const colKeys = columns.map((col) =>
      typeof col === "string" ? col : col.key || col.label || "",
    );

    // Body
    const tbody = document.createElement("tbody");
    for (const row of rows) {
      const tr = document.createElement("tr");
      if (Array.isArray(row)) {
        for (let i = 0; i < columns.length; i++) {
          const td = createElement(
            "td",
            {
              style: {
                padding: "8px 12px",
                borderBottom: "1px solid " + THEME.border,
                color: THEME.textPrimary,
              },
            },
            row[i] !== undefined ? String(row[i]) : "",
          );
          tr.appendChild(td);
        }
      } else if (row && typeof row === "object") {
        for (const key of colKeys) {
          const val = Object.hasOwn(row, key) ? row[key] : "";
          const td = createElement(
            "td",
            {
              style: {
                padding: "8px 12px",
                borderBottom: "1px solid " + THEME.border,
                color: THEME.textPrimary,
              },
            },
            val !== undefined && val !== null ? String(val) : "",
          );
          tr.appendChild(td);
        }
      }
      tbody.appendChild(tr);
    }
    table.appendChild(tbody);

    // Sortable: add click handlers to header cells
    if (props.sortable) {
      const thCells = headerRow.querySelectorAll("th");
      let sortCol = -1;
      let sortAsc = true;

      for (let ci = 0; ci < thCells.length; ci++) {
        ((colIndex) => {
          thCells[colIndex].addEventListener("click", () => {
            if (sortCol === colIndex) {
              sortAsc = !sortAsc;
            } else {
              sortCol = colIndex;
              sortAsc = true;
            }
            const bodyRows = Array.from(tbody.children);
            bodyRows.sort((a, b) => {
              const aText = a.children[colIndex]
                ? a.children[colIndex].textContent
                : "";
              const bText = b.children[colIndex]
                ? b.children[colIndex].textContent
                : "";
              const aNum = Number(aText);
              const bNum = Number(bText);
              if (!isNaN(aNum) && !isNaN(bNum)) {
                return sortAsc ? aNum - bNum : bNum - aNum;
              }
              return sortAsc
                ? aText.localeCompare(bText)
                : bText.localeCompare(aText);
            });
            // Re-append sorted rows
            for (const r of bodyRows) tbody.appendChild(r);
          });
        })(ci);
      }
    }

    wrapper.appendChild(table);
    return wrapper;
  }

  // --- key-value ---
  function renderKeyValue(props) {
    const wrapper = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        padding: "20px",
      },
    });

    if (props.title) {
      wrapper.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "14px",
              fontWeight: "600",
              marginBottom: "12px",
              color: THEME.textPrimary,
            },
          },
          props.title,
        ),
      );
    }

    const items = Array.isArray(props.items) ? props.items : [];
    const isGrid = props.layout === "grid";

    const container = createElement("div", {
      style: isGrid
        ? {
            display: "grid",
            gridTemplateColumns: "repeat(auto-fill, minmax(200px, 1fr))",
            gap: "12px",
          }
        : { display: "flex", flexDirection: "column", gap: "8px" },
    });

    for (const item of items) {
      if (!item || typeof item !== "object") continue;

      if (isGrid) {
        const cell = createElement("div", {
          style: {
            background: THEME.card,
            borderRadius: "6px",
            padding: "10px",
          },
        });
        cell.appendChild(
          createElement(
            "div",
            {
              style: {
                fontSize: "10px",
                color: THEME.textSecondary,
                textTransform: "uppercase",
                letterSpacing: "0.5px",
                marginBottom: "2px",
              },
            },
            item.key,
          ),
        );
        cell.appendChild(
          createElement(
            "div",
            {
              style: {
                fontSize: "14px",
                fontWeight: "600",
                fontFamily: THEME.fontMono,
                color: resolveColor(item.color, THEME.textPrimary),
              },
            },
            item.value,
          ),
        );
        container.appendChild(cell);
      } else {
        const row = createElement("div", {
          style: {
            display: "grid",
            gridTemplateColumns: "minmax(80px, 35%) 1fr",
            gap: "8px",
            padding: "6px 0",
            borderBottom: "1px solid " + THEME.border,
          },
        });
        row.appendChild(
          createElement(
            "span",
            {
              style: { color: THEME.textSecondary, fontSize: "13px" },
            },
            item.key,
          ),
        );
        row.appendChild(
          createElement(
            "span",
            {
              style: {
                fontFamily: THEME.fontMono,
                fontSize: "13px",
                fontWeight: "500",
                color: resolveColor(item.color, THEME.textPrimary),
              },
            },
            item.value,
          ),
        );
        container.appendChild(row);
      }
    }

    wrapper.appendChild(container);
    return wrapper;
  }

  // Safe HTML renderer: parses content with DOMParser and copies only allowlisted
  // elements/text nodes into the target container — no innerHTML on live DOM.
  const SAFE_TEXT_TAGS = new Set([
    "p", "br", "strong", "em", "b", "i",
    "ul", "ol", "li",
    "code", "span", "div",
    "h1", "h2", "h3", "h4", "h5", "h6",
    "blockquote", "hr",
  ]);

  // Convert inline markdown spans (**bold**, *italic*, `code`) to HTML.
  function inlineMarkdownToHTML(text) {
    return text
      .replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")
      .replace(/\*([^*\n]+)\*/g, "<em>$1</em>")
      .replace(/`([^`\n]+)`/g, "<code>$1</code>");
  }

  // Convert a markdown string to an HTML string. Handles paragraphs,
  // unordered lists (- / *), ordered lists (1. 2. …), and inline markup.
  function markdownToHTML(md) {
    const blocks = String(md).split(/\n{2,}/);
    return blocks
      .map((block) => {
        const trimmed = block.trim();
        if (!trimmed) return "";
        const lines = trimmed.split("\n");

        // Unordered list
        if (lines.every((l) => /^\s*[-*]\s/.test(l) || !l.trim())) {
          const items = lines
            .filter((l) => /^\s*[-*]\s/.test(l))
            .map((l) => "<li>" + inlineMarkdownToHTML(l.replace(/^\s*[-*]\s+/, "")) + "</li>");
          return "<ul>" + items.join("") + "</ul>";
        }
        // Ordered list
        if (lines.every((l) => /^\s*\d+[.)]\s/.test(l) || !l.trim())) {
          const items = lines
            .filter((l) => /^\s*\d+[.)]\s/.test(l))
            .map((l) => "<li>" + inlineMarkdownToHTML(l.replace(/^\s*\d+[.)]\s+/, "")) + "</li>");
          return "<ol>" + items.join("") + "</ol>";
        }
        // Plain paragraph — join lines with a space
        return "<p>" + inlineMarkdownToHTML(lines.join(" ")) + "</p>";
      })
      .join("");
  }

  // Returns true if the string looks like markdown rather than HTML.
  function looksLikeMarkdown(s) {
    const hasMarkdownSyntax = /\*\*|\*[^*]|`[^`]|^\s*[-*]\s|^\s*\d+[.)]\s/m.test(s);
    const hasHTMLTags = /<[a-zA-Z][^>]*>/.test(s);
    return hasMarkdownSyntax && !hasHTMLTags;
  }

  function appendSafeHTML(container, html) {
    if (!html) return;
    const raw = String(html);
    // Auto-convert markdown so old LLM-generated specs still render correctly.
    const markup = looksLikeMarkdown(raw) ? markdownToHTML(raw) : raw;
    let doc;
    try {
      doc = new DOMParser().parseFromString(
        "<div>" + markup + "</div>",
        "text/html",
      );
    } catch (_) {
      container.textContent = raw;
      return;
    }

    function copyNodes(src, dst) {
      for (const child of src.childNodes) {
        if (child.nodeType === Node.TEXT_NODE) {
          dst.appendChild(document.createTextNode(child.textContent));
        } else if (child.nodeType === Node.ELEMENT_NODE) {
          const tag = child.tagName.toLowerCase();
          if (SAFE_TEXT_TAGS.has(tag)) {
            const el = document.createElement(tag);
            copyNodes(child, el);
            dst.appendChild(el);
          } else {
            // Unknown tag: flatten children into parent
            copyNodes(child, dst);
          }
        }
      }
    }

    copyNodes(doc.body.firstChild, container);
  }

  // --- text ---
  function renderText(props) {
    const styleMap = {
      note: {
        bg: THEME.surface,
        border: THEME.accent,
        color: THEME.accent,
      },
      warning: {
        bg: "#2d2a1a",
        border: THEME.warning,
        color: THEME.warning,
      },
      error: {
        bg: "#2d1a1e",
        border: THEME.error,
        color: THEME.error,
      },
      default: {
        bg: "transparent",
        border: "transparent",
        color: THEME.textPrimary,
      },
    };
    const s = styleMap[props.style] || styleMap.default;

    const el = createElement("div", {
      style: {
        padding: s.bg === "transparent" ? "4px 0" : "12px 16px",
        background: s.bg,
        borderLeft:
          s.border === "transparent" ? "none" : "3px solid " + s.border,
        borderRadius: s.bg === "transparent" ? "0" : "6px",
        color: s.color,
        fontSize: "13px",
        lineHeight: "1.6",
      },
    });
    appendSafeHTML(el, props.content || "");
    return el;
  }

  // --- code-block ---
  function renderCodeBlock(props) {
    const wrapper = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        overflow: "hidden",
      },
    });

    if (props.title || props.language) {
      const header = createElement("div", {
        style: {
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          padding: "8px 16px",
          borderBottom: "1px solid " + THEME.border,
          background: THEME.card,
        },
      });
      if (props.title) {
        header.appendChild(
          createElement(
            "span",
            {
              style: {
                fontSize: "12px",
                fontWeight: "600",
                color: THEME.textPrimary,
              },
            },
            props.title,
          ),
        );
      }
      if (props.language) {
        header.appendChild(
          createElement(
            "span",
            {
              style: {
                fontSize: "11px",
                color: THEME.textMuted,
                fontFamily: THEME.fontMono,
              },
            },
            props.language,
          ),
        );
      }
      wrapper.appendChild(header);
    }

    const pre = createElement("pre", {
      style: {
        padding: "16px",
        margin: "0",
        overflowX: "auto",
        fontFamily: THEME.fontMono,
        fontSize: "12px",
        lineHeight: "1.6",
        color: THEME.textPrimary,
        whiteSpace: "pre-wrap",
        wordBreak: "break-word",
      },
    });
    const code = createElement("code", null, props.code || "");
    pre.appendChild(code);
    wrapper.appendChild(pre);

    return wrapper;
  }

  // --- progress-bar ---
  function renderProgressBar(props) {
    const wrapper = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        padding: "20px",
      },
    });

    if (props.title) {
      wrapper.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "14px",
              fontWeight: "600",
              marginBottom: "12px",
              color: THEME.textPrimary,
            },
          },
          props.title,
        ),
      );
    }

    const thresholds = props.thresholds || { high: 90, medium: 60 };
    const items = Array.isArray(props.items) ? props.items : [];

    for (const item of items) {
      if (!item || typeof item !== "object") continue;
      const value = Math.max(0, Math.min(100, Number(item.value) || 0));

      const row = createElement("div", {
        style: {
          display: "flex",
          alignItems: "center",
          gap: "12px",
          marginBottom: "8px",
        },
      });

      row.appendChild(
        createElement(
          "div",
          {
            style: {
              width: "160px",
              fontFamily: THEME.fontMono,
              fontSize: "12px",
              color: THEME.textPrimary,
              textAlign: "right",
              whiteSpace: "nowrap",
              overflow: "hidden",
              textOverflow: "ellipsis",
              flexShrink: "0",
            },
          },
          item.label,
        ),
      );

      const track = createElement("div", {
        style: {
          flex: "1",
          height: "20px",
          background: THEME.bg,
          borderRadius: "4px",
          overflow: "hidden",
          position: "relative",
        },
      });

      let barColor;
      if (item.color) {
        barColor = resolveColor(item.color);
      } else if (value >= (thresholds.high || 90)) {
        barColor = THEME.success;
      } else if (value >= (thresholds.medium || 60)) {
        barColor = THEME.warning;
      } else {
        barColor = THEME.error;
      }

      const fill = createElement("div", {
        style: {
          height: "100%",
          width: value + "%",
          background: barColor,
          borderRadius: "4px",
          transition: "width 0.4s ease",
        },
      });
      track.appendChild(fill);
      row.appendChild(track);

      row.appendChild(
        createElement(
          "div",
          {
            style: {
              width: "60px",
              fontFamily: THEME.fontMono,
              fontSize: "12px",
              textAlign: "right",
              color: barColor,
              flexShrink: "0",
            },
          },
          value.toFixed(1) + "%",
        ),
      );

      wrapper.appendChild(row);
    }

    return wrapper;
  }

  // --- badges ---
  function renderBadges(props) {
    const container = createElement("div", {
      style: { display: "flex", gap: "8px", flexWrap: "wrap", alignSelf: "start", height: "fit-content" },
    });
    const items = Array.isArray(props.items) ? props.items : [];
    for (const item of items) {
      if (!item || typeof item !== "object") continue;
      const color = resolveColor(item.color, THEME.accent);
      const badge = createElement(
        "span",
        {
          style: {
            display: "inline-block",
            padding: "3px 10px",
            borderRadius: "12px",
            fontSize: "11px",
            fontWeight: "600",
            fontFamily: THEME.fontMono,
            background: color + "22",
            color: color,
            border: "1px solid " + color + "44",
          },
        },
        item.text || item.label,
      );
      container.appendChild(badge);
    }
    return container;
  }

  // --- heatmap color scales ---
  // String shortcuts map to {low, high} pairs using the Tokyonight palette.
  const HEATMAP_SCALES = Object.freeze({
    blue: { low: THEME.surface, high: THEME.cyan },
    green: { low: THEME.surface, high: THEME.success },
    red: { low: THEME.surface, high: THEME.error },
  });

  function resolveHeatmapScale(scale) {
    if (!scale) return { low: THEME.success, high: THEME.error };
    if (typeof scale === "string") {
      return HEATMAP_SCALES[scale] || HEATMAP_SCALES.blue;
    }
    // Object form: {low, high} with optional named color resolution
    return {
      low: scale.low || THEME.success,
      high: scale.high || THEME.error,
    };
  }

  // --- heatmap ---
  function renderHeatmap(props) {
    const wrapper = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        padding: "20px",
        overflowX: "auto",
      },
    });

    if (props.title) {
      wrapper.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "14px",
              fontWeight: "600",
              marginBottom: "12px",
              color: THEME.textPrimary,
            },
          },
          props.title,
        ),
      );
    }

    const rowLabels =
      Array.isArray(props.rowLabels) ? props.rowLabels :
      Array.isArray(props.yLabels)   ? props.yLabels :
      Array.isArray(props.y_labels)  ? props.y_labels :
      Array.isArray(props.rows)      ? props.rows : [];
    const columnLabels =
      Array.isArray(props.columnLabels) ? props.columnLabels :
      Array.isArray(props.xLabels)      ? props.xLabels :
      Array.isArray(props.x_labels)     ? props.x_labels :
      Array.isArray(props.columns)      ? props.columns : [];
    const values = Array.isArray(props.values)
      ? props.values
      : Array.isArray(props.data)
        ? props.data
        : [];
    const colorScale = resolveHeatmapScale(props.colorScale);

    const grid = createElement("div", {
      style: {
        display: "inline-grid",
        gap: "2px",
        gridTemplateColumns: "100px repeat(" + columnLabels.length + ", 80px)",
        fontFamily: THEME.fontMono,
        fontSize: "11px",
      },
    });

    // Header row
    grid.appendChild(
      createElement("div", {
        style: {
          height: "32px",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        },
      }),
    );
    for (const colLabel of columnLabels) {
      grid.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "10px",
              color: THEME.textSecondary,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              height: "32px",
              minWidth: "80px",
              overflow: "hidden",
              textOverflow: "ellipsis",
            },
          },
          colLabel,
        ),
      );
    }

    // Data rows
    for (let ri = 0; ri < rowLabels.length; ri++) {
      // Row label
      grid.appendChild(
        createElement(
          "div",
          {
            style: {
              minWidth: "100px",
              height: "32px",
              display: "flex",
              alignItems: "center",
              justifyContent: "flex-end",
              paddingRight: "8px",
              fontSize: "12px",
              color: THEME.textPrimary,
            },
          },
          rowLabels[ri],
        ),
      );

      const rowValues = Array.isArray(values[ri]) ? values[ri] : [];
      for (let ci = 0; ci < columnLabels.length; ci++) {
        const val = ci < rowValues.length ? Number(rowValues[ci]) || 0 : 0;
        const cellColor = interpolateHeatColor(val, colorScale);

        grid.appendChild(
          createElement(
            "div",
            {
              style: {
                minWidth: "80px",
                height: "32px",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                borderRadius: "3px",
                background: cellColor,
                color: val > 50 ? "#ffffff" : THEME.textPrimary,
                fontSize: "10px",
              },
            },
            String(val),
          ),
        );
      }
    }

    wrapper.appendChild(grid);
    return wrapper;
  }

  function interpolateHeatColor(value, scale) {
    // Clamp 0-100 and interpolate from low to high color
    const t = Math.max(0, Math.min(100, value)) / 100;
    const lowRGB = hexToRGB(resolveColor(scale.low, THEME.success));
    const highRGB = hexToRGB(resolveColor(scale.high, THEME.error));
    const r = Math.round(lowRGB.r + (highRGB.r - lowRGB.r) * t);
    const g = Math.round(lowRGB.g + (highRGB.g - lowRGB.g) * t);
    const b = Math.round(lowRGB.b + (highRGB.b - lowRGB.b) * t);
    // Use moderate opacity so background shows through a bit
    return "rgba(" + r + "," + g + "," + b + ",0.6)";
  }

  function hexToRGB(hex) {
    const stripped = hex.replace("#", "");
    let r, g, b;
    if (stripped.length === 3) {
      r = parseInt(stripped[0] + stripped[0], 16);
      g = parseInt(stripped[1] + stripped[1], 16);
      b = parseInt(stripped[2] + stripped[2], 16);
    } else {
      r = parseInt(stripped.substring(0, 2), 16);
      g = parseInt(stripped.substring(2, 4), 16);
      b = parseInt(stripped.substring(4, 6), 16);
    }
    return { r: r || 0, g: g || 0, b: b || 0 };
  }

  // --- header ---
  function renderHeader(props) {
    const container = createElement("div", {
      style: {
        display: "flex",
        alignItems: "center",
        gap: "12px",
        marginBottom: "8px",
        paddingBottom: "16px",
        borderBottom: "1px solid " + THEME.border,
      },
    });

    container.appendChild(
      createElement(
        "h1",
        {
          style: {
            fontSize: "20px",
            fontWeight: "600",
            color: THEME.textPrimary,
            margin: "0",
          },
        },
        props.title || "",
      ),
    );

    if (props.badge) {
      container.appendChild(
        createElement(
          "span",
          {
            style: {
              fontSize: "11px",
              padding: "2px 8px",
              borderRadius: "4px",
              background: "#3d59a1",
              color: THEME.accent,
              fontFamily: THEME.fontMono,
            },
          },
          props.badge,
        ),
      );
    }

    if (props.description) {
      // Place description below the header line
      const wrap = createElement("div");
      wrap.appendChild(container);
      wrap.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "12px",
              color: THEME.textSecondary,
              fontFamily: THEME.fontMono,
              marginBottom: "8px",
            },
          },
          props.description,
        ),
      );
      return wrap;
    }

    return container;
  }

  // --- section ---
  function renderSection(props, children, depth, gen) {
    // When there are no children the LLM used `section` as a flat divider/heading.
    // Render a lightweight title row instead of an empty container card.
    if (!Array.isArray(children) || children.length === 0) {
      const divider = createElement("div", {
        style: {
          borderBottom: "2px solid " + THEME.border,
          paddingBottom: "10px",
          marginTop: "4px",
        },
      });
      divider.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "16px",
              fontWeight: "600",
              color: THEME.textPrimary,
            },
          },
          props.title || "",
        ),
      );
      if (props.subtitle) {
        divider.appendChild(
          createElement(
            "div",
            {
              style: {
                fontSize: "12px",
                color: THEME.textSecondary,
                marginTop: "2px",
              },
            },
            props.subtitle,
          ),
        );
      }
      return Promise.resolve(divider);
    }

    const section = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        padding: "20px",
      },
    });

    const headerRow = createElement("div", {
      style: {
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        marginBottom: "16px",
      },
    });

    const titleWrap = createElement("div");
    titleWrap.appendChild(
      createElement(
        "div",
        {
          style: {
            fontSize: "16px",
            fontWeight: "600",
            color: THEME.textPrimary,
          },
        },
        props.title || "",
      ),
    );

    if (props.subtitle) {
      titleWrap.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "12px",
              color: THEME.textSecondary,
              marginTop: "2px",
            },
          },
          props.subtitle,
        ),
      );
    }
    headerRow.appendChild(titleWrap);

    const contentContainer = createElement("div");

    if (props.collapsible) {
      let collapsed = false;
      const toggle = createElement(
        "button",
        {
          style: {
            background: "none",
            border: "1px solid " + THEME.border,
            borderRadius: "4px",
            color: THEME.textSecondary,
            padding: "4px 10px",
            cursor: "pointer",
            fontFamily: THEME.fontMono,
            fontSize: "11px",
          },
        },
        "Collapse",
      );

      toggle.addEventListener("click", () => {
        collapsed = !collapsed;
        contentContainer.style.display = collapsed ? "none" : "block";
        toggle.textContent = collapsed ? "Expand" : "Collapse";
      });
      headerRow.appendChild(toggle);
    }

    section.appendChild(headerRow);

    // Render children into content container (async handled by caller via await)
    const childPromise = renderComponents(
      children,
      contentContainer,
      depth + 1,
      gen,
    );
    section.appendChild(contentContainer);

    // Return a promise that resolves to the section element after children render
    return childPromise.then(() => section);
  }

  // --- tabs ---
  function renderTabs(props, children, depth, gen) {
    const tabDefs = Array.isArray(props.tabs) ? props.tabs : [];
    const childList = Array.isArray(children) ? children : [];

    const container = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        padding: "20px",
      },
    });

    // Tab buttons
    const tabBar = createElement("div", {
      style: {
        display: "flex",
        gap: "4px",
        marginBottom: "16px",
        borderBottom: "1px solid " + THEME.border,
        paddingBottom: "8px",
      },
    });

    const panels = [];
    const buttons = [];

    for (let i = 0; i < tabDefs.length; i++) {
      const tabDef = tabDefs[i];
      const btn = createElement(
        "button",
        {
          style: {
            padding: "6px 14px",
            border: "none",
            borderRadius: "4px 4px 0 0",
            background: i === 0 ? THEME.accent : "transparent",
            color: i === 0 ? THEME.bg : THEME.textSecondary,
            fontSize: "12px",
            fontWeight: "500",
            cursor: "pointer",
            fontFamily: THEME.fontSans,
          },
        },
        tabDef.label || "Tab " + (i + 1),
      );
      buttons.push(btn);

      const panel = createElement("div", {
        style: { display: i === 0 ? "block" : "none" },
      });
      panels.push(panel);

      ((index) => {
        btn.addEventListener("click", () => {
          for (let j = 0; j < buttons.length; j++) {
            buttons[j].style.background =
              j === index ? THEME.accent : "transparent";
            buttons[j].style.color =
              j === index ? THEME.bg : THEME.textSecondary;
            panels[j].style.display = j === index ? "block" : "none";
          }
        });
      })(i);

      tabBar.appendChild(btn);
    }

    container.appendChild(tabBar);

    // Each child goes into the corresponding panel
    const renderPromises = [];
    for (let i = 0; i < tabDefs.length; i++) {
      const child = i < childList.length ? childList[i] : null;
      if (child) {
        // Wrap single child in an array for renderComponents
        const childArray = Array.isArray(child) ? child : [child];
        renderPromises.push(
          renderComponents(childArray, panels[i], depth + 1, gen),
        );
      } else if (tabDefs[i] && tabDefs[i].content) {
        // Fallback: render content string directly when no children supplied
        appendSafeHTML(panels[i], String(tabDefs[i].content));
      }
      container.appendChild(panels[i]);
    }

    return Promise.all(renderPromises).then(() => container);
  }

  // --- dag ---
  function renderDAG(props) {
    const wrapper = createElement("div", {
      style: {
        background: THEME.surface,
        border: "1px solid " + THEME.border,
        borderRadius: "8px",
        padding: "24px",
        overflowX: "auto",
      },
    });

    if (props.title) {
      wrapper.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "14px",
              fontWeight: "600",
              marginBottom: "16px",
              color: THEME.textPrimary,
            },
          },
          props.title,
        ),
      );
    }

    const nodes = Array.isArray(props.nodes) ? props.nodes : [];
    const edges = Array.isArray(props.edges) ? props.edges : [];

    if (nodes.length === 0) {
      wrapper.appendChild(
        createElement(
          "div",
          {
            style: {
              color: THEME.textMuted,
              fontFamily: THEME.fontMono,
              fontSize: "12px",
            },
          },
          "No nodes to display",
        ),
      );
      return wrapper;
    }

    // Build adjacency and compute layers via topological ordering
    const nodeMap = new Map();
    for (const node of nodes) {
      if (node && node.id !== undefined) {
        nodeMap.set(String(node.id), node);
      }
    }

    const childrenOf = new Map(); // parent -> [child]
    const parentCount = new Map(); // node -> number of parents
    for (const node of nodes) {
      const id = String(node.id);
      parentCount.set(id, 0);
      childrenOf.set(id, []);
    }
    for (const edge of edges) {
      if (!edge || edge.from === undefined || edge.to === undefined) continue;
      const from = String(edge.from);
      const to = String(edge.to);
      if (!nodeMap.has(from) || !nodeMap.has(to)) continue;
      childrenOf.get(from).push(to);
      parentCount.set(to, (parentCount.get(to) || 0) + 1);
    }

    // BFS layering
    const layers = [];
    const layerOf = new Map();
    const queue = [];
    for (const [id, count] of parentCount) {
      if (count === 0) queue.push(id);
    }

    while (queue.length > 0) {
      const id = queue.shift();
      const parentLayer = layerOf.has(id) ? layerOf.get(id) : 0;
      // Place this node
      if (!layerOf.has(id)) layerOf.set(id, 0);
      const myLayer = layerOf.get(id);
      while (layers.length <= myLayer) layers.push([]);
      layers[myLayer].push(id);

      for (const child of childrenOf.get(id)) {
        const childLayer = Math.max(layerOf.get(child) || 0, myLayer + 1);
        layerOf.set(child, childLayer);
        const remaining = parentCount.get(child) - 1;
        parentCount.set(child, remaining);
        if (remaining <= 0) queue.push(child);
      }
    }

    // Dimensions
    const NODE_W = 200;
    const NODE_H = 60;
    const H_GAP = 40;
    const V_GAP = 50;

    let maxCols = 1;
    for (const layer of layers) {
      if (layer.length > maxCols) maxCols = layer.length;
    }

    const totalWidth = maxCols * (NODE_W + H_GAP) - H_GAP + 48;
    const totalHeight = layers.length * (NODE_H + V_GAP) - V_GAP + 48;

    const svg = createSVGElement("svg", {
      width: totalWidth,
      height: totalHeight,
      viewBox: "0 0 " + totalWidth + " " + totalHeight,
    });
    if (!svg) return wrapper;

    // Arrowhead marker
    const defs = createSVGElement("defs", {});
    const marker = createSVGElement("marker", {
      id: "dag-arrow",
      markerWidth: "10",
      markerHeight: "7",
      refX: "10",
      refY: "3.5",
      orient: "auto",
    });
    const arrowPoly = createSVGElement("polygon", {
      points: "0 0, 10 3.5, 0 7",
      fill: THEME.textSecondary,
    });
    if (marker && arrowPoly) marker.appendChild(arrowPoly);
    if (defs && marker) defs.appendChild(marker);
    if (defs) svg.appendChild(defs);

    // Compute positions
    const posMap = new Map();
    for (let r = 0; r < layers.length; r++) {
      const cols = layers[r].length;
      const rowWidth = cols * (NODE_W + H_GAP) - H_GAP;
      const startX = (totalWidth - rowWidth) / 2;
      for (let c = 0; c < cols; c++) {
        const id = layers[r][c];
        posMap.set(id, {
          x: startX + c * (NODE_W + H_GAP),
          y: 24 + r * (NODE_H + V_GAP),
        });
      }
    }

    // Draw edges
    const edgesGroup = createSVGElement("g", {});
    if (edgesGroup) {
      for (const edge of edges) {
        if (!edge) continue;
        const fromId = String(edge.from);
        const toId = String(edge.to);
        const fromPos = posMap.get(fromId);
        const toPos = posMap.get(toId);
        if (!fromPos || !toPos) continue;

        const x1 = fromPos.x + NODE_W / 2;
        const y1 = fromPos.y + NODE_H;
        const x2 = toPos.x + NODE_W / 2;
        const y2 = toPos.y;
        const midY = (y1 + y2) / 2;

        const path = createSVGElement("path", {
          d:
            "M " +
            x1 +
            " " +
            y1 +
            " C " +
            x1 +
            " " +
            midY +
            ", " +
            x2 +
            " " +
            midY +
            ", " +
            x2 +
            " " +
            y2,
          fill: "none",
          stroke: THEME.textSecondary,
          "stroke-width": "2",
          "marker-end": "url(#dag-arrow)",
        });
        if (path) edgesGroup.appendChild(path);
      }
      svg.appendChild(edgesGroup);
    }

    // Draw nodes
    const nodesGroup = createSVGElement("g", {});
    if (nodesGroup) {
      for (const [id, pos] of posMap) {
        const node = nodeMap.get(id);
        if (!node) continue;

        const color = resolveColor(node.color, THEME.accent);

        const group = createSVGElement("g", {
          transform: "translate(" + pos.x + "," + pos.y + ")",
          class: "dag-node",
        });
        if (!group) continue;

        // Background rect
        const rect = createSVGElement("rect", {
          width: NODE_W,
          height: NODE_H,
          rx: "8",
          ry: "8",
          fill: THEME.card,
          stroke: color,
          "stroke-width": "2",
        });
        if (rect) group.appendChild(rect);

        // Label
        const labelText = createSVGText(
          "text",
          {
            x: NODE_W / 2,
            y: node.sublabel ? 24 : 34,
            fill: THEME.textPrimary,
            "font-family": THEME.fontSans,
            "font-size": "13",
            "font-weight": "600",
            "text-anchor": "middle",
          },
          truncateText(node.label || id, 24),
        );
        if (labelText) group.appendChild(labelText);

        // Sublabel
        if (node.sublabel) {
          const subText = createSVGText(
            "text",
            {
              x: NODE_W / 2,
              y: 42,
              fill: THEME.textSecondary,
              "font-family": THEME.fontMono,
              "font-size": "10",
              "text-anchor": "middle",
            },
            truncateText(node.sublabel, 30),
          );
          if (subText) group.appendChild(subText);
        }

        nodesGroup.appendChild(group);
      }
      svg.appendChild(nodesGroup);
    }

    wrapper.appendChild(svg);
    return wrapper;
  }

  function truncateText(text, maxLen) {
    if (!text) return "";
    const s = String(text);
    if (s.length <= maxLen) return s;
    return s.substring(0, maxLen - 2) + "..";
  }

  // --- message-list ---
  function renderMessageList(props) {
    const container = createElement("div", {
      style: {
        display: "flex",
        flexDirection: "column",
        gap: "12px",
      },
    });

    const messages = Array.isArray(props.messages) ? props.messages : [];
    // User bubble has a fixed dark background so always use white text,
    // regardless of the active theme.
    const userTextColor = "#ffffff";

    for (const msg of messages) {
      if (!msg || typeof msg !== "object") continue;

      // Accept common LLM-generated field name aliases so specs that use
      // "from"/"sender"/"type" for role, or "text"/"message" for content,
      // still render correctly rather than showing "unknown".
      const role = msg.role || msg.from || msg.sender || msg.author || "";
      const content = msg.content || msg.text || msg.message || msg.body || "";
      const timestamp = msg.timestamp || msg.time || msg.ts || null;

      const isUser = role === "user";
      const isSystem = role === "system";

      const bubble = createElement("div", {
        style: {
          maxWidth: isSystem ? "100%" : "80%",
          alignSelf: isUser ? "flex-end" : "flex-start",
          background: isUser
            ? "#3d59a1"
            : isSystem
              ? THEME.card
              : THEME.surface,
          border: "1px solid " + (isUser ? THEME.accent : THEME.border),
          borderRadius: isUser ? "12px 12px 4px 12px" : "12px 12px 12px 4px",
          padding: "12px 16px",
        },
      });

      // Role label
      const roleLabel = createElement(
        "div",
        {
          style: {
            fontSize: "10px",
            fontWeight: "600",
            textTransform: "uppercase",
            letterSpacing: "0.5px",
            marginBottom: "4px",
            color: isUser
              ? userTextColor
              : isSystem
                ? THEME.warning
                : THEME.success,
          },
        },
        role || "unknown",
      );
      bubble.appendChild(roleLabel);

      // Content
      bubble.appendChild(
        createElement(
          "div",
          {
            style: {
              fontSize: "13px",
              lineHeight: "1.6",
              color: isUser ? userTextColor : THEME.textPrimary,
              whiteSpace: "pre-wrap",
              wordBreak: "break-word",
            },
          },
          content,
        ),
      );

      // Timestamp
      if (timestamp) {
        bubble.appendChild(
          createElement(
            "div",
            {
              style: {
                fontSize: "10px",
                color: isUser ? "rgba(255,255,255,0.65)" : THEME.textMuted,
                marginTop: "6px",
                fontFamily: THEME.fontMono,
                textAlign: isUser ? "right" : "left",
              },
            },
            timestamp,
          ),
        );
      }

      container.appendChild(bubble);
    }

    return container;
  }

  // ---------------------------------------------------------------------------
  // 10. MCP postMessage protocol (TOFU)
  // ---------------------------------------------------------------------------

  let mcpRequestId = 0;
  const mcpPending = new Map();
  let trustedOrigin = null;

  function sendMCPRequest(method, params) {
    return new Promise((resolve, reject) => {
      const id = ++mcpRequestId;
      mcpPending.set(id, { resolve, reject });
      const targetOrigin = trustedOrigin || "*";
      window.parent.postMessage(
        {
          jsonrpc: "2.0",
          id,
          method,
          params: params || {},
        },
        targetOrigin,
      );
      setTimeout(() => {
        if (mcpPending.has(id)) {
          mcpPending.delete(id);
          reject(new Error("Request timed out: " + method));
        }
      }, 30000);
    });
  }

  function handlePostMessage(event) {
    // TOFU: once trusted, reject other origins
    if (trustedOrigin && event.origin !== trustedOrigin) return;

    const data = event.data;
    if (!data || typeof data !== "object") return;

    // Handle data updates matching the spec's data_type
    if (
      APP_SPEC.data_type &&
      data.type === APP_SPEC.data_type &&
      data.payload
    ) {
      handleDataUpdate(data.payload, data.target_component_id || null);
      return;
    }

    // JSON-RPC 2.0 responses
    if (data.jsonrpc !== "2.0") return;

    if (typeof data.id === "number" && mcpPending.has(data.id)) {
      if (!trustedOrigin && event.origin) trustedOrigin = event.origin;
      const handler = mcpPending.get(data.id);
      mcpPending.delete(data.id);
      if (data.error) {
        handler.reject(new Error(data.error.message || "Unknown error"));
      } else {
        handler.resolve(data.result);
      }
      return;
    }

    // Notifications
    if (
      typeof data.method === "string" &&
      data.method.startsWith("ui/notifications/")
    ) {
      if (data.method === "ui/notifications/host-context-changed") {
        handleThemeChange(data.params);
      }
    }
  }

  window.addEventListener("message", handlePostMessage);

  // Apply the current THEME values to CSS custom properties and body background.
  // Called on initial render and every time the host switches themes so that
  // CSS-variable-driven rules (e.g. table row striping) always match THEME.
  function applyThemeVars() {
    const root = document.documentElement;
    root.style.setProperty("--bg-primary", THEME.bg);
    root.style.setProperty("--bg-surface", THEME.surface);
    root.style.setProperty("--bg-card", THEME.card);
    root.style.setProperty("--bg-elevated", THEME.elevated);
    root.style.setProperty("--bg-hover", THEME.hover);
    root.style.setProperty("--border", THEME.border);
    root.style.setProperty("--text-primary", THEME.textPrimary);
    root.style.setProperty("--text-secondary", THEME.textSecondary);
    root.style.setProperty("--text-muted", THEME.textMuted);
    root.style.setProperty("--accent", THEME.accent);
    root.style.setProperty("--success", THEME.success);
    root.style.setProperty("--warning", THEME.warning);
    root.style.setProperty("--error", THEME.error);
    root.style.setProperty("--cyan", THEME.cyan);
    root.style.setProperty("--magenta", THEME.magenta);
    document.body.style.backgroundColor = THEME.bg;
  }

  function handleThemeChange(params) {
    if (!params || !params.theme) return;
    const palette = THEMES[params.theme] || THEMES.dark;
    Object.assign(THEME, palette);
    applyThemeVars();
    // Re-render charts — they bake colors at canvas-render time.
    destroyAllCharts();
    fullRender();
  }

  // ---------------------------------------------------------------------------
  // 11. Data update handling
  // ---------------------------------------------------------------------------

  function handleDataUpdate(payload, targetComponentId) {
    if (targetComponentId) {
      // Targeted update: find component by data-component-id and re-render just that subtree
      const targetEl = APP_ROOT.querySelector(
        "[data-component-id='" + CSS.escape(targetComponentId) + "']",
      );
      if (!targetEl) {
        console.warn(
          "runtime.js: target component not found:",
          targetComponentId,
        );
        return;
      }

      // Find the matching component in the spec
      const comp = findComponentById(APP_SPEC.components, targetComponentId);
      if (!comp) {
        console.warn("runtime.js: component not in spec:", targetComponentId);
        return;
      }

      // Merge payload into props and re-render in place
      const mergedProps = Object.assign({}, comp.props || {}, payload);
      const renderFn = Object.hasOwn(COMPONENT_REGISTRY, comp.type)
        ? COMPONENT_REGISTRY[comp.type]
        : null;
      if (!renderFn) return;

      try {
        const result = renderFn(mergedProps, comp.children, 0);
        const applyResult = (newEl) => {
          if (newEl && targetEl.parentNode) {
            if (comp.id)
              setSafeAttribute(newEl, "data-component-id", String(comp.id));
            targetEl.parentNode.replaceChild(newEl, targetEl);
          }
        };
        if (result instanceof Promise) {
          result.then(applyResult).catch((err) => {
            console.error("runtime.js: targeted update error:", err);
          });
        } else {
          applyResult(result);
        }
      } catch (err) {
        console.error("runtime.js: targeted update error:", err);
      }
    } else {
      // Full re-render: clear and rebuild
      fullRender();
    }
  }

  function findComponentById(components, id) {
    if (!Array.isArray(components)) return null;
    for (const comp of components) {
      if (!comp || typeof comp !== "object") continue;
      if (comp.id === id) return comp;
      if (Array.isArray(comp.children)) {
        const found = findComponentById(comp.children, id);
        if (found) return found;
      }
    }
    return null;
  }

  // ---------------------------------------------------------------------------
  // 12. Initial render
  // ---------------------------------------------------------------------------

  function fullRender() {
    // Sync CSS custom properties and body background to the current THEME
    // so that CSS-variable-driven rules (table stripes, body bg, etc.) are
    // correct from the very first paint, not just after a theme-change message.
    applyThemeVars();

    // Destroy existing chart instances
    destroyAllCharts();

    // Clear app root safely
    while (APP_ROOT.firstChild) {
      APP_ROOT.removeChild(APP_ROOT.firstChild);
    }

    // Apply layout to root. Auto-upgrade stack → grid-2 when the spec has
    // multiple chart components so they sit side-by-side without the LLM
    // needing to explicitly choose a grid layout.
    const _specLayout = APP_SPEC.layout || "stack";
    const _chartCount = Array.isArray(APP_SPEC.components)
      ? APP_SPEC.components.filter((c) => c && c.type === "chart").length
      : 0;
    const _layout =
      _specLayout === "stack" && _chartCount >= 2 ? "grid-2" : _specLayout;
    applyLayout(APP_ROOT, _layout);

    // Render all components — capture generation so stale async renders abort.
    const gen = ++renderGen;
    renderComponents(APP_SPEC.components, APP_ROOT, 0, gen).catch((err) => {
      if (renderGen !== gen) return;
      console.error("runtime.js: render error:", err);
      const errBox = createElement(
        "div",
        {
          style: {
            color: THEME.error,
            background: THEME.card,
            border: "1px solid " + THEME.error,
            borderRadius: "6px",
            padding: "16px",
            fontFamily: THEME.fontMono,
            fontSize: "13px",
          },
        },
        "Fatal render error: " + String(err.message || err),
      );
      APP_ROOT.appendChild(errBox);
    });
  }

  async function initialize() {
    try {
      await sendMCPRequest("ui/initialize", {
        appUri: APP_SPEC.uri || "",
        capabilities: { resize: true },
      });
    } catch (_) {
      // Host may not support ui/initialize -- non-fatal
    }
    fullRender();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initialize);
  } else {
    initialize();
  }

  // Notify host of size changes
  if (typeof ResizeObserver !== "undefined") {
    new ResizeObserver(() => {
      try {
        sendMCPRequest("ui/notifications/size-changed", {
          width: document.body.scrollWidth,
          height: document.body.scrollHeight,
        });
      } catch (_) {
        /* ignore */
      }
    }).observe(document.body);
  }
})();
