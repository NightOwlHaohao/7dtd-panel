import { t } from "./i18n.js";
import { pushActivity, pushLog, refreshStatus } from "./store.js";
import { toast } from "./ui.js";

// Server-sent events: log lines feed the log views, state changes refresh
// /api/status (coalesced), and operational events feed the activity list.

let statusTimer;
function scheduleStatusRefresh() {
  clearTimeout(statusTimer);
  statusTimer = setTimeout(() => refreshStatus().catch(() => {}), 150);
}

export function handleEvent(event) {
  const time = event.time ? new Date(event.time) : new Date();
  switch (event.type) {
    case "log":
      if (event.message) pushLog(event.message);
      break;
    case "server":
      scheduleStatusRefresh();
      if (event.data?.state) pushActivity({ time, source: "server", message: t(`state_${event.data.state}`) });
      break;
    case "update":
    case "backup":
    case "save":
    case "schedule":
      if (event.message) pushActivity({ time, source: event.type, message: event.message, data: event.data });
      break;
    default:
      break;
  }
}

let current = null;

/** Close the stream for good, e.g. after the panel was stopped. */
export function closeEvents() {
  current?.close();
  current = null;
}

export function connectEvents() {
  const source = new EventSource("/api/events");
  current = source;
  let lost = false;
  source.onopen = () => {
    if (lost) {
      toast(t("eventReconnected"), "success");
      scheduleStatusRefresh();
    }
    lost = false;
  };
  source.onerror = () => {
    if (!lost) toast(t("eventDisconnected"), "error");
    lost = true;
  };
  source.onmessage = message => {
    try {
      handleEvent(JSON.parse(message.data));
    } catch {
      // Ignore malformed frames; the next status refresh resynchronises.
    }
  };
  return source;
}
