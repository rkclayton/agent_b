export function updateAvailableText(update) {
  return update?.available && update.version ? `${update.version} available` : "";
}
