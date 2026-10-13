export function actionTable(names) {
  return Object.fromEntries(names.map((name) => [name, (context) => context.run(name)]));
}
