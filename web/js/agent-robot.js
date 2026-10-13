const drawing = '<svg viewBox="0 0 20 20" aria-hidden="true"><path class="agent-robot-line" d="M6.25 18.5h7.5M10 15.75v2.5"/><g class="agent-robot-head"><path class="agent-robot-line" d="M10 4.25V3M1.75 8.25v3.5M18.25 8.25v3.5"/><circle class="agent-robot-lamp" cx="10" cy="1.85" r="1.15"/><rect class="agent-robot-shell" x="3.5" y="4.5" width="13" height="11" rx="2.6"/><g class="agent-robot-eyes"><rect class="agent-robot-eye" x="6" y="7.5" width="2.6" height="2.6" rx=".7"/><rect class="agent-robot-eye" x="11.4" y="7.5" width="2.6" height="2.6" rx=".7"/></g><path class="agent-robot-mouth" d="M7.6 12.6h4.8"/></g></svg>';

export function createAgentRobot(document, { place = "chat" } = {}) {
  const robot = document.createElement("span");
  robot.dataset.robotPlace = place;
  robot.innerHTML = drawing;
  if (place === "chat") robot.setAttribute("aria-hidden", "true");
  else robot.setAttribute("role", "img");
  updateAgentRobot(robot, { state: "idle" });
  return robot;
}

export function updateAgentRobot(robot, { state = "idle", doing = "", label } = {}) {
  const place = robot.dataset.robotPlace === "shell" ? "shell" : "chat";
  robot.className = `agent-robot ${place === "shell" ? "shell-app-robot" : "chat-live-robot"} ${state}`;
  if (doing) robot.dataset.doing = doing; else delete robot.dataset.doing;
  if (label !== undefined) {
    robot.title = label;
    robot.setAttribute("aria-label", label);
  }
}
