/**
 * 命令实现注册表 —— path（`<service> <subcommand>`，去掉位置参数占位）→ handler。
 *
 * program.ts 按 path 查表；未注册的命令回落 notImplemented 占位。
 * 新增 service 实现时在此登记即可，命令树结构仍以 command-tree.ts 为准。
 */
import type { CommandHandler } from "../program.js";
import { configInit, configShow, configSet, configUnset } from "./config.js";
import { profileList, profileUse, profileCreate, profileDelete } from "./profile.js";
import {
  authAddApiKey,
  authListApiKeys,
  authRemoveApiKey,
  authSetApiKey,
  authSetDefaultApiKey,
  authStatus,
  authTokenRotate,
  authCheck,
} from "./auth.js";
import {
  notificationSearch,
  notificationSummary,
  notificationStats,
  notificationStoragePath,
  notificationToday,
  notificationRecent,
  notificationUnread,
} from "./notification.js";
import { syncScan, syncFetch, syncCommit } from "./sync.js";
import {
  recordingList,
  recordingStatus,
  recordingStoragePath,
  recordingLatest,
  recordingSetupAsr,
  recordingEvents,
} from "./recording.js";
import {
  imageList,
  imageStatus,
  imagePath,
  imageStoragePath,
  imageLatest,
} from "./image.js";
import { logSearch, logErrors } from "./log.js";
import { migrateFromOpenclaw } from "./migrate.js";
import { updateSelf } from "./update.js";
import { doctor } from "./doctor.js";
import { skillsList, skillsTargets, skillsInstall } from "./skills.js";
import {
  daemonStart,
  daemonStop,
  daemonRestart,
  daemonReload,
  daemonStatus,
  daemonLogs,
  daemonRunForeground,
} from "./daemon.js";
import {
  lightSend,
  lightBlink,
  lightruleList,
  lightruleShow,
  lightruleCreate,
  lightruleUpdate,
  lightruleDelete,
  lightruleEnable,
  lightruleDisable,
  lightruleOn,
  lightruleOff,
  tunnelStatus,
  tunnelReconnect,
  tunnelTest,
  monitorList,
  monitorShow,
  monitorCreate,
  monitorDelete,
  monitorEnable,
  monitorDisable,
  gatewayTest,
  apiRaw,
} from "./daemon-services.js";

export const HANDLERS: Record<string, CommandHandler> = {
  "config init": configInit,
  "config show": configShow,
  "config set": configSet,
  "config unset": configUnset,

  "profile list": profileList,
  "profile use": profileUse,
  "profile create": profileCreate,
  "profile delete": profileDelete,

  "auth set-api-key": authSetApiKey,
  "auth add-api-key": authAddApiKey,
  "auth list-api-keys": authListApiKeys,
  "auth remove-api-key": authRemoveApiKey,
  "auth set-default-api-key": authSetDefaultApiKey,
  "auth status": authStatus,
  "auth token-rotate": authTokenRotate,
  "auth check": authCheck,

  "notification search": notificationSearch,
  "notification summary": notificationSummary,
  "notification stats": notificationStats,
  "notification storage-path": notificationStoragePath,
  "notification +today": notificationToday,
  "notification +recent": notificationRecent,
  "notification +unread": notificationUnread,

  "sync scan": syncScan,
  "sync fetch": syncFetch,
  "sync commit": syncCommit,

  "recording list": recordingList,
  "recording status": recordingStatus,
  "recording storage-path": recordingStoragePath,
  "recording setup-asr": recordingSetupAsr,
  "recording events": recordingEvents,
  "recording +latest": recordingLatest,

  "image list": imageList,
  "image status": imageStatus,
  "image path": imagePath,
  "image storage-path": imageStoragePath,
  "image +latest": imageLatest,

  log: logSearch,
  "log +errors": logErrors,

  "migrate from-openclaw": migrateFromOpenclaw,
  "update self": updateSelf,
  "skills list": skillsList,
  "skills targets": skillsTargets,
  "skills install": skillsInstall,
  doctor,

  "daemon start": daemonStart,
  "daemon stop": daemonStop,
  "daemon restart": daemonRestart,
  "daemon reload": daemonReload,
  "daemon status": daemonStatus,
  "daemon logs": daemonLogs,
  "daemon run-foreground": daemonRunForeground,

  "light send": lightSend,
  "light +blink": lightBlink,

  "lightrule list": lightruleList,
  "lightrule show": lightruleShow,
  "lightrule create": lightruleCreate,
  "lightrule update": lightruleUpdate,
  "lightrule delete": lightruleDelete,
  "lightrule enable": lightruleEnable,
  "lightrule disable": lightruleDisable,
  "lightrule +on": lightruleOn,
  "lightrule +off": lightruleOff,

  "tunnel status": tunnelStatus,
  "tunnel reconnect": tunnelReconnect,
  "tunnel +test": tunnelTest,

  "monitor list": monitorList,
  "monitor show": monitorShow,
  "monitor create": monitorCreate,
  "monitor delete": monitorDelete,
  "monitor enable": monitorEnable,
  "monitor disable": monitorDisable,

  "gateway test": gatewayTest,
  api: apiRaw,
};
