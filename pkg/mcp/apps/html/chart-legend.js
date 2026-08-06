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

// chart-legend.js - Custom HTML legend for Chart.js charts.
//
// This file is NOT a standalone script. It is spliced into runtime.js's IIFE at
// build time by the Go compiler (replacing the __LOOM_INJECT_CHART_LEGEND__
// marker), so it shares that closure's scope and helpers:
//   - THEME, createElement, setSafeAttribute (from runtime.js)
//   - SLICE_CHART_TYPES (from runtime.js)
//
// Why a custom legend: the default Chart.js legend is drawn on the canvas and
// cannot scroll, so many series/slices overflow onto the plot. This builds a
// themed HTML legend instead:
//   - A horizontal "carousel": items live in a nowrap track inside an
//     overflow-hidden viewport, with ‹ / › buttons that scroll left/right.
//   - Scroll buttons auto-hide when there is nothing to scroll in a direction.
//   - Long labels are truncated with an ellipsis; the full text is exposed via
//     the native `title` tooltip.
//   - Clicking an item toggles series/slice visibility and dims + strikes
//     through the hidden item.
//
// Built with createElement/textContent/setSafeAttribute only (no innerHTML).

// Derive legend items directly from the chart's data/config. This intentionally
// does NOT use Chart.js's default generateLabels(): when the on-canvas legend is
// disabled (display:false) Chart.js never creates chart.legend, and the default
// generateLabels reads chart.legend.options and throws. Building items by hand
// works for both per-slice charts (pie/doughnut/polarArea, one item per label)
// and per-dataset charts (bar/line/radar/etc., one item per dataset).
function legendItemsFromChart(chart) {
  const data = chart && chart.data ? chart.data : {};
  const datasets = Array.isArray(data.datasets) ? data.datasets : [];
  const chartType =
    chart && chart.config && chart.config.type ? chart.config.type : "";
  const isSlice = SLICE_CHART_TYPES.has(chartType);
  const items = [];

  function colorAt(value, i) {
    return Array.isArray(value) ? value[i] : value;
  }

  if (isSlice) {
    const labels = Array.isArray(data.labels) ? data.labels : [];
    const ds0 = datasets[0] || {};
    for (let i = 0; i < labels.length; i++) {
      const hidden =
        typeof chart.getDataVisibility === "function"
          ? !chart.getDataVisibility(i)
          : false;
      items.push({
        text: String(labels[i] != null ? labels[i] : ""),
        fillStyle: colorAt(ds0.backgroundColor, i),
        strokeStyle: colorAt(ds0.borderColor, i),
        index: i,
        slice: true,
        hidden: hidden,
      });
    }
  } else {
    for (let i = 0; i < datasets.length; i++) {
      const ds = datasets[i] || {};
      let hidden = false;
      if (typeof chart.getDatasetMeta === "function") {
        const meta = chart.getDatasetMeta(i);
        hidden =
          meta && meta.hidden !== null ? meta.hidden : Boolean(ds.hidden);
      }
      items.push({
        text: String(
          ds.label != null && ds.label !== "" ? ds.label : "Series " + (i + 1),
        ),
        fillStyle: ds.backgroundColor || ds.borderColor,
        strokeStyle: ds.borderColor || ds.backgroundColor,
        datasetIndex: i,
        slice: false,
        hidden: hidden,
      });
    }
  }
  return items;
}

function buildCustomLegend(chart) {
  if (!chart || !chart.data) return null;

  const items = legendItemsFromChart(chart);
  if (!Array.isArray(items) || items.length === 0) return null;

  // Suppress a noisy single-item legend for an unlabeled single-series chart —
  // it adds a meaningless "Series 1" chip. Slice charts and any labeled/
  // multi-series chart still get a legend.
  const datasets = Array.isArray(chart.data.datasets)
    ? chart.data.datasets
    : [];
  const meaningful =
    items[0].slice ||
    datasets.length > 1 ||
    datasets.some((ds) => ds && ds.label);
  if (!meaningful) return null;

  const container = createElement("div", {
    className: "tera-chart-legend",
    style: {
      display: "flex",
      alignItems: "center",
      gap: "4px",
      marginTop: "12px",
    },
  });

  function makeScrollButton(text, dir) {
    return createElement(
      "button",
      {
        type: "button",
        "aria-label": dir < 0 ? "Scroll legend left" : "Scroll legend right",
        style: {
          flex: "0 0 auto",
          width: "20px",
          height: "20px",
          // Start hidden; updateButtons() reveals them only when overflow exists.
          display: "none",
          alignItems: "center",
          justifyContent: "center",
          padding: "0",
          border: "1px solid " + THEME.border,
          borderRadius: "4px",
          background: THEME.card,
          color: THEME.textSecondary,
          cursor: "pointer",
          fontFamily: THEME.fontMono,
          fontSize: "12px",
          lineHeight: "1",
        },
      },
      text,
    );
  }

  const leftBtn = makeScrollButton("‹", -1);
  const rightBtn = makeScrollButton("›", 1);

  // Viewport clips the track; the track scrolls horizontally.
  const viewport = createElement("div", {
    style: {
      flex: "1 1 auto",
      overflow: "hidden",
      position: "relative",
    },
  });
  const track = createElement("div", {
    className: "tera-chart-legend-track",
    style: {
      display: "flex",
      flexWrap: "nowrap",
      gap: "12px",
      overflowX: "auto",
      scrollBehavior: "smooth",
      // Hide the native scrollbar; navigation is via the ‹ / › buttons.
      scrollbarWidth: "none",
      msOverflowStyle: "none",
    },
  });
  viewport.appendChild(track);

  for (const item of items) {
    const entry = createElement("div", {
      className: "tera-chart-legend-item",
      style: {
        display: "flex",
        alignItems: "center",
        gap: "6px",
        flex: "0 0 auto",
        cursor: "pointer",
        userSelect: "none",
        opacity: item.hidden ? "0.4" : "1",
      },
    });

    const swatch = createElement("span", {
      style: {
        flex: "0 0 auto",
        width: "10px",
        height: "10px",
        borderRadius: "2px",
        background: String(item.fillStyle || item.strokeStyle || THEME.accent),
        border: "1px solid " + String(item.strokeStyle || THEME.border),
      },
    });

    const labelText = String(item.text != null ? item.text : "");
    const label = createElement(
      "span",
      {
        style: {
          fontFamily: THEME.fontMono,
          fontSize: "11px",
          color: THEME.textSecondary,
          maxWidth: "140px",
          overflow: "hidden",
          textOverflow: "ellipsis",
          whiteSpace: "nowrap",
          textDecoration: item.hidden ? "line-through" : "none",
        },
      },
      labelText,
    );
    // Native tooltip exposes the full (possibly truncated) label text.
    setSafeAttribute(label, "title", labelText);

    entry.appendChild(swatch);
    entry.appendChild(label);

    entry.addEventListener("click", () => {
      let nowHidden;
      if (item.slice && typeof chart.toggleDataVisibility === "function") {
        // Slice charts (pie/doughnut/polarArea) have a single dataset with
        // per-item visibility. getDataVisibility returns the CURRENT visibility;
        // after toggling it flips, so the new hidden state equals the current
        // visibility value.
        nowHidden = chart.getDataVisibility
          ? chart.getDataVisibility(item.index)
          : true;
        chart.toggleDataVisibility(item.index);
        chart.update();
      } else if (
        typeof chart.setDatasetVisibility === "function" &&
        typeof chart.isDatasetVisible === "function"
      ) {
        // Per-dataset charts: use the public visibility API (matches Chart.js's
        // own HTML-legend sample) so stacking/animations stay consistent.
        const visible = chart.isDatasetVisible(item.datasetIndex);
        chart.setDatasetVisibility(item.datasetIndex, !visible);
        nowHidden = visible;
        chart.update();
      } else {
        return;
      }
      entry.style.opacity = nowHidden ? "0.4" : "1";
      label.style.textDecoration = nowHidden ? "line-through" : "none";
    });

    track.appendChild(entry);
  }

  leftBtn.addEventListener("click", () => {
    track.scrollBy({ left: -Math.max(120, track.clientWidth * 0.6) });
  });
  rightBtn.addEventListener("click", () => {
    track.scrollBy({ left: Math.max(120, track.clientWidth * 0.6) });
  });

  // Show/hide scroll buttons based on whether the track overflows and where it
  // is currently scrolled.
  function updateButtons() {
    const overflowing = track.scrollWidth > track.clientWidth + 1;
    const atStart = track.scrollLeft <= 1;
    const atEnd = track.scrollLeft + track.clientWidth >= track.scrollWidth - 1;
    leftBtn.style.display = overflowing && !atStart ? "flex" : "none";
    rightBtn.style.display = overflowing && !atEnd ? "flex" : "none";
  }

  track.addEventListener("scroll", updateButtons);
  if (typeof ResizeObserver === "function") {
    const ro = new ResizeObserver(updateButtons);
    ro.observe(track);
  }
  // Initial state after layout settles.
  setTimeout(updateButtons, 0);

  container.appendChild(leftBtn);
  container.appendChild(viewport);
  container.appendChild(rightBtn);
  return container;
}
