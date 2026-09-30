import { api } from "../api.js";
import { formatDateTime } from "../format.js";
import { html } from "../html.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { callout, confirmAction, emptyState, pageHeader, toast, withBusy } from "../ui.js";

const ACTIONS = ["restart", "backup", "command", "say"];
const DAYS = [1, 2, 3, 4, 5, 6, 0]; // Monday first

const PRESETS = {
  restart: () => ({ name: t("taskPreset_restart"), enabled: true, action: "restart", times: ["04:00"], warnings: [10, 5, 1] }),
  backup: () => ({ name: t("taskPreset_backup"), enabled: true, action: "backup", every: 360, keep: 10 }),
  command: () => ({ name: t("taskPreset_command"), enabled: true, action: "command", command: "saveworld", every: 30 }),
  say: () => ({ name: t("taskPreset_say"), enabled: true, action: "say", command: "", every: 60 }),
};

const listText = values => (values || []).join(", ");
const parseList = text => text.split(/[,，\s]+/).map(value => value.trim()).filter(Boolean);

function statusLine(status) {
  if (!status) return "";
  const parts = [];
  if (status.running) parts.push(html`<span class="badge badge-info">${t("taskRunning")}</span>`);
  if (status.nextRun) parts.push(html`<span>${t("taskNext", { time: formatDateTime(status.nextRun) })}</span>`);
  if (status.lastRun) {
    const tone = { success: "badge-success", skipped: "", error: "badge-danger" }[status.lastResult] || "";
    parts.push(html`<span>${t("taskLast", { time: formatDateTime(status.lastRun) })} <span class="badge ${tone}">${t(`taskResult_${status.lastResult}`)}</span></span>`);
  }
  return html`<p class="subtle">${parts}</p>${status.lastError ? html`<p class="subtle">${status.lastError}</p>` : ""}`;
}

function taskCard(task, index, status) {
  const timed = !(task.every > 0);
  const commandLabel = { command: "taskCommand", say: "taskMessage", restart: "taskWarningText" }[task.action];
  return html`<section class="card" data-task="${index}" data-id="${task.id || ""}">
    <div class="card-header">
      <h3>${task.name || t("taskUnnamed")}</h3>
      <label class="check"><input type="checkbox" data-field="enabled" ${task.enabled ? html`checked` : ""}>${t("taskEnabled")}</label>
      ${statusLine(status)}
    </div>
    <div class="card-body stack">
      <div class="row">
        <label class="field field-grow"><span>${t("taskName")}</span><input data-field="name" value="${task.name || ""}" maxlength="100"></label>
        <label class="field"><span>${t("taskAction")}</span><select data-field="action">${ACTIONS.map(action => html`<option value="${action}" ${action === task.action ? html`selected` : ""}>${t(`taskAction_${action}`)}</option>`)}</select></label>
      </div>
      ${commandLabel ? html`<label class="field"><span>${t(commandLabel)}</span><input data-field="command" value="${task.command || ""}" maxlength="500" spellcheck="false" placeholder="${task.action === "restart" ? "Server restart in {minutes} min" : ""}"></label>` : ""}
      <div class="row">
        <label class="field"><span>${t("taskTiming")}</span><select data-field="mode">
          <option value="times" ${timed ? html`selected` : ""}>${t("taskAtTimes")}</option>
          <option value="every" ${timed ? "" : html`selected`}>${t("taskEvery")}</option></select></label>
        ${timed
          ? html`<label class="field field-grow"><span>${t("taskTimes")}</span><input data-field="times" value="${listText(task.times)}" placeholder="04:00, 16:00" spellcheck="false"></label>`
          : html`<label class="field"><span>${t("taskEveryMinutes")}</span><input data-field="every" type="number" min="1" max="10080" value="${task.every}"></label>`}
      </div>
      ${timed ? html`<fieldset class="chip-row"><legend class="subtle">${t("taskDays")}</legend>
        ${DAYS.map(day => html`<label class="check"><input type="checkbox" data-day="${day}" ${!(task.days || []).length || task.days.includes(day) ? html`checked` : ""}>${t(`day_${day}`)}</label>`)}</fieldset>` : ""}
      ${task.action === "restart" ? html`<label class="field"><span>${t("taskWarnings")}</span><input data-field="warnings" value="${listText(task.warnings)}" placeholder="10, 5, 1"></label><p class="field-hint">${t("taskWarningsHelp")}</p>` : ""}
      ${task.action === "backup" ? html`<label class="field"><span>${t("taskKeep")}</span><input data-field="keep" type="number" min="1" max="500" value="${task.keep || 10}"></label><p class="field-hint">${t("taskKeepHelp")}</p>` : ""}
      <div class="button-group">
        <button type="button" class="button button-secondary button-sm" data-run="${task.id || ""}" ${task.id ? "" : html`disabled title="${t("taskSaveFirst")}"`}>${icon("play")}${t("taskRunNow")}</button>
        <button type="button" class="button button-ghost button-sm" data-remove="${index}">${icon("trash")}${t("delete")}</button>
      </div>
    </div>
  </section>`;
}

function readTask(card, previous) {
  const field = name => card.querySelector(`[data-field="${name}"]`);
  const task = {
    id: card.dataset.id || undefined,
    name: field("name").value,
    enabled: field("enabled").checked,
    action: field("action").value,
    command: field("command")?.value ?? previous.command ?? "",
  };
  if (field("mode").value === "every") {
    task.every = Number(field("every")?.value ?? previous.every ?? 60) || 60;
  } else {
    task.times = field("times") ? parseList(field("times").value) : previous.times || ["04:00"];
    const days = [...card.querySelectorAll("[data-day]")];
    const checked = days.filter(box => box.checked).map(box => Number(box.dataset.day));
    task.days = days.length && checked.length < 7 ? checked : [];
  }
  if (field("warnings")) task.warnings = parseList(field("warnings").value).map(Number);
  else if (task.action === "restart") task.warnings = previous.warnings || [];
  if (field("keep")) task.keep = Number(field("keep").value);
  return task;
}

export default {
  async render(ctx) {
    let data = await api.get("/api/schedule", { signal: ctx.signal });
    let tasks = structuredClone(data.settings.tasks || []);
    let dirty = false;
    ctx.setGuard(() => dirty);

    const statusById = () => Object.fromEntries((data.status || []).map(status => [status.id, status]));

    const collect = () => {
      const page = ctx.main.querySelector("#schedule-page");
      return {
        autoStartServer: page.querySelector("#auto-start").checked,
        crashRestart: page.querySelector("#crash-restart").checked,
        tasks: [...page.querySelectorAll("[data-task]")].map(card => readTask(card, tasks[Number(card.dataset.task)] || {})),
      };
    };

    const paint = (settings = data.settings) => {
      const statuses = statusById();
      ctx.main.innerHTML = html`<div id="schedule-page">
        ${pageHeader(t("nav_schedule"), t("scheduleHelp"))}
        <div class="stack-lg">
          <section class="card">
            <div class="card-header"><h3>${t("scheduleGeneral")}</h3></div>
            <div class="card-body stack">
              <label class="check"><input type="checkbox" id="crash-restart" ${settings.crashRestart ? html`checked` : ""}>${t("scheduleCrashRestart")}</label>
              <p class="field-hint">${t("scheduleCrashRestartHelp")}</p>
              <label class="check"><input type="checkbox" id="auto-start" ${settings.autoStartServer ? html`checked` : ""}>${t("scheduleAutoStart")}</label>
              <p class="field-hint">${t("scheduleAutoStartHelp")} <a href="#/panel">${t("nav_panel")}</a></p>
            </div>
          </section>
          <div class="row-between">
            <h3>${t("scheduleTasks")}</h3>
            <div class="button-group">${ACTIONS.map(action => html`<button type="button" class="button button-secondary button-sm" data-add="${action}">${icon("plus")}${t(`taskAction_${action}`)}</button>`)}</div>
          </div>
          ${tasks.length ? tasks.map((task, index) => taskCard(task, index, statuses[task.id])) : emptyState("clock", t("scheduleEmpty"), t("scheduleEmptyHelp"))}
          ${callout("info", "info", t("scheduleTimeNote", { time: formatDateTime(data.now) }))}
          <div class="save-bar"><button class="button button-primary" id="schedule-save">${t("save")}</button></div>
        </div>
      </div>`.value;
      bind();
    };

    const repaintFromForm = () => {
      const current = collect();
      tasks = current.tasks;
      paint({ ...data.settings, ...current });
      dirty = true;
    };

    const bind = () => {
      const page = ctx.main.querySelector("#schedule-page");
      page.addEventListener("input", () => { dirty = true; });
      for (const select of page.querySelectorAll('[data-field="action"], [data-field="mode"]')) select.addEventListener("change", repaintFromForm);
      for (const button of page.querySelectorAll("[data-add]")) {
        button.addEventListener("click", () => {
          const current = collect();
          tasks = [...current.tasks, PRESETS[button.dataset.add]()];
          paint({ ...data.settings, ...current });
          dirty = true;
          ctx.main.querySelector("[data-task]:last-of-type input[data-field=name]")?.focus();
        });
      }
      for (const button of page.querySelectorAll("[data-remove]")) {
        button.addEventListener("click", async () => {
          if (!(await confirmAction({ title: t("taskDeleteTitle"), message: t("taskDeleteConfirm"), confirmLabel: t("delete"), danger: true }))) return;
          const current = collect();
          current.tasks.splice(Number(button.dataset.remove), 1);
          tasks = current.tasks;
          paint({ ...data.settings, ...current });
          dirty = true;
        });
      }
      for (const button of page.querySelectorAll("[data-run]")) {
        button.addEventListener("click", async () => {
          if (dirty) { toast(t("taskSaveFirst"), "error"); return; }
          if (!(await confirmAction({ title: t("taskRunNowTitle"), message: t("taskRunNowConfirm"), confirmLabel: t("taskRunNow") }))) return;
          await withBusy(button, t("working"), async () => {
            data.status = await api.post(`/api/schedule/tasks/${encodeURIComponent(button.dataset.run)}/run`);
            toast(t("taskStarted"), "success");
          });
        });
      }
      page.querySelector("#schedule-save").addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
        data = await api.put("/api/schedule", collect());
        tasks = structuredClone(data.settings.tasks || []);
        dirty = false;
        toast(t("saved"), "success");
        paint();
      }));
    };

    paint();
    // Refresh next/last run times when a task reports, unless editing.
    ctx.on("activity", entry => {
      if (entry.source !== "schedule" || dirty) return;
      api.get("/api/schedule").then(next => {
        if (!ctx.isCurrent() || dirty) return;
        data = next;
        tasks = structuredClone(data.settings.tasks || []);
        paint();
      }).catch(() => {});
    });
  },
};
