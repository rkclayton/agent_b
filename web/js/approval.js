export function shellGrantApproval(data = {}) {
  const boundary = typeof data.boundary_escape === "boolean"
    ? data.boundary_escape
    : data.name?.endsWith(".operator_override");
  return (!boundary && data.name === "shell")
    || data.name === "shell.operator_override"
    || data.name === "shell.operator_command";
}

export function approvalChoices(data = {}) {
  if (shellGrantApproval(data)) {
    return [["once", "Once"], ["run", "For this run"], ["operator_mode", "Operator mode"], ["deny", "Keep denied"]];
  }
  if (data.boundary_escape) return [["approve", "Run once as operator"], ["deny", "Keep denied"]];
  return [["approve", "Approve"], ["deny", "Deny"]];
}
