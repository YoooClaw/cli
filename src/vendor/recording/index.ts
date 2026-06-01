export {
  RecordingStorage,
  resolveRecordingStorageDir,
  type RecordingIndexEntry,
  type RecordingStorageContext,
} from "./storage.js";

export {
  TRANSCRIPT_DOCUMENT_SCHEMA_VERSION,
  buildTranscriptDataFilename,
  buildTranscriptDocument,
  parseTranscriptDocument,
  extractTranscriptTextFromDocument,
  extractTranscriptSummaryFromDocument,
  extractTranscriptTitleFromDocument,
  extractSourceTextListFromDocument,
  type TranscriptDocument,
  type TranscriptDocumentContentItem,
  type TranscriptDocumentNormalized,
  type TranscriptDocumentSegment,
  type TranscriptDocumentSource,
} from "./transcript-document.js";

export {
  isValidTransition,
  transition,
  isTerminalStatus,
  canStartTranscription,
  getNextStatuses,
  TransitionError,
} from "./state-machine.js";

export {
  downloadFile,
  downloadRecordingFiles,
  type DownloadResult,
  type DownloadOptions,
} from "./downloader.js";

export {
  isAsrConfigured,
  validateAsrConfig,
  initializeAsr,
  transcribeAudio,
  buildTranscriptMarkdown,
  extractSummary,
  runTranscriptionWorkflow,
  type TranscriptionResult,
  type TranscriptSegment,
} from "./asr.js";

export {
  detectEnvironment,
  selectModelByMemory,
  isModelDownloaded,
  downloadModel,
  findWhisperBinary,
  transcribeWithWhisperLocal,
  getWhisperLocalStatus,
} from "./whisper-local.js";

export {
  handleRecordingSync,
  triggerTranscription,
  type RecordingStatusEvent,
  type RecordingSyncOptions,
} from "./handler.js";
