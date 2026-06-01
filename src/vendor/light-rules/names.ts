export const LIGHT_RULE_GATEWAY_METHODS = {
  list: "lightrules.list",
  create: "lightrules.create",
  update: "lightrules.update",
  delete: "lightrules.delete",
} as const;

export const LIGHT_RULE_GATEWAY_METHOD_LIST = [
  LIGHT_RULE_GATEWAY_METHODS.list,
  LIGHT_RULE_GATEWAY_METHODS.create,
  LIGHT_RULE_GATEWAY_METHODS.update,
  LIGHT_RULE_GATEWAY_METHODS.delete,
] as const;

export const LIGHT_RULE_TOOL_NAMES = {
  list: "lightrules_list",
  create: "lightrules_create",
  update: "lightrules_update",
  delete: "lightrules_delete",
} as const;

export const LIGHT_RULE_TOOL_NAME_LIST = [
  LIGHT_RULE_TOOL_NAMES.list,
  LIGHT_RULE_TOOL_NAMES.create,
  LIGHT_RULE_TOOL_NAMES.update,
  LIGHT_RULE_TOOL_NAMES.delete,
] as const;
