// api/index.ts —— 前端 API 的统一出口（P2-C2）。
//
// src/api.ts 已近 1900 行，新同事定位一个接口要在里面翻很久；本轮按域拆到同级文件，
// 但**只动了文件边界**：对外暴露的符号一个没少、一个没改名，这里全部原样转出，
// 因此 `import { listProjects } from './api'` 这类既有写法不受影响（目录解析自动命中本文件）。
// 新增接口请直接落到对应域文件并在下方补一行转出。

export { setScriptLanguagePreference, setSourceRevisionPreference } from './identity';
export type { ClientIdentity } from './identity';
export { getAuthConfig, registerEmail, loginEmail, logoutSession, verifyEmail, resendVerification, forgotPassword, resetPassword } from './auth';
export type { EmailAuthResult, AuthConfig } from './auth';
export { getPlaybackManifest } from './playback';
export { listProjects, listArchivedProjects, listAllProjects, restoreProject, listProjectsPage, getProject, createProject, archiveProject, getProjectSlides, getSourceRevisions, deleteSourceRevision, updatePptDisplayName, downloadSourceRevision, getSlideNotes, setSlideNotes, getSlideRenderURLs } from './projects';
export type { ArchivedProject, ProjectPage, SourceRevisionSummary, SlideRenderURL } from './projects';
export { setSlideScriptSource, getSlideScriptSources, getScript, listProjectScripts, updateScript, regenerateSegments, rewriteScriptText, regenerateScriptDraft } from './scripts';
export type { SlideScriptSource, UpdateScriptResult, RewriteAction } from './scripts';
export { createGeneration, estimateNarration, getNarrationDraftCount, getProjectArtifacts, getLibraryArtifacts, getArtifactManifest, deleteArtifact, getRevisionVoiceStatus, getNarrationStale, getNarration, getRevisionNarration } from './narration';
export type { NarrationEstimate, ProjectArtifact, LibraryArtifact, NarrationSlideStale, RevisionVoiceStatus, SynthesisStats, NarrationStatus } from './narration';
export { createUpload, uploadToURL, completeUpload, abortUpload } from './upload';
export type { UploadSession, CompletedUpload } from './upload';
export { listAuditEvents, listAuditArchives, sha256Hex } from './audit';
export { listGateways, createGateway, updateGateway, deleteGateway, setDefaultGateway, testGateway } from './gateway';
export type { ModelGateway, GatewayTestResult, GatewayScope } from './gateway';
export { getVoiceSettings, saveVoiceSettings, listVoiceModels } from './voice';
export type { ProjectVoiceSettings, VoiceModel } from './voice';
export { listJobs, listJobsPage, getJob, getJobDetail, getJobsSummary, getJobsPage, cancelJob, retryFailedJob, watchJobEvents } from './jobs';
export type { JobPage, JobStepState, JobStep, JobScope, JobExtras, JobDetail, JobListSort, JobListRow, JobsPageResult, JobEventMessage } from './jobs';
export { listMembers, updateMemberProfile, listTags, createTag, renameTag, deleteTag, listFolders, createFolder, renameFolder, deleteFolder, listProjectOrganization, attachTag, detachTag, moveProject, setMemberRole, removeMember, getQuota, getUsage, getProjectUsage, getStorageUsage, getPolicy } from './tenant';
export { createExport, createDownload } from './export';
export { listDictionaries, createDictionary, updateDictionary, deleteDictionary } from './pronunciation';
export { listContextRules, createContextRule, updateContextRule, deleteContextRule } from './contextrules';
export { listPublicWorks, getShowcaseWork, getShowcaseManifest, publishWork, featureWork, reviewWork, deleteWork, recallWork, listMyPublications, listReviewQueue } from './public';
export type { PublicationKind, PublicationStatus, PublicWork, PublicWorkPage } from './public';
export { listCollaborators, inviteCollaborator, updateCollaboratorRole, removeCollaborator, listShareLinks, createShareLink, revokeShareLink, getSharedMeta, getSharedManifest, shareUrl } from './share';
export { listAdminTenants, suspendTenant, resumeTenant } from './admin';
export type { AdminTenantQuota, AdminTenant, AdminTenantsPage } from './admin';
export { getMessageChannels, saveEmailChannel, testEmailChannel, saveSmsChannel } from './messages';
export type { MessageEmailConfig, MessageSMSConfig, MessageChannels, EmailChannelInput, SMSChannelInput } from './messages';

// ConnectError 由 core.ts 定义（P2-C1 把错误处理从业务 API 层降到 HTTP 核心层），
// 仍从这里转出以保持既有 import 路径不变；新代码建议直接从 ./core 引入。
export { ConnectError } from '../core';
