package assistant

const (
	TuningAgentFileCapBytes    = 32 << 20
	TuningAgentMessageCapBytes = 64 << 20
	TuningAgentFileNameChars   = 80

	TuningAgentGrafanaTimeoutSeconds = 15
	TuningAgentGrafanaMaxSeries      = 20
	TuningAgentGrafanaMaxMetricRows  = 50
	TuningAgentGrafanaMaxLogRows     = 200
	TuningAgentGrafanaMaxAlerts      = 50
	TuningAgentGrafanaMaxDashboards  = 20
	TuningAgentGrafanaCellChars      = 300

	TuningAgentJournalLeaseSeconds = 1800

	TuningAgentKbProfileTokens     = 2000
	TuningAgentKbRetrievalTokens   = 1500
	TuningAgentKbRetrievalDistance = 0.35
	TuningAgentKbWriteChars        = 4000

	TuningAgentMailMaxResults    = 20
	TuningAgentMailSnippetChars  = 300
	TuningAgentMailBodyChars     = 8000
	TuningAgentMailMaxLoopsLimit = 100

	TuningAgentMediaTorrentLimit = 50

	TuningAgentManifestResults = 30
	TuningAgentPreviewChars    = 600
	TuningAgentStubTokens      = 2000

	TuningAgentNightlyMaxThreads  = 30
	TuningAgentNightlyThreadChars = 3000
	TuningAgentNightlyTotalChars  = 40000

	TuningAgentProjectNameChars        = 80
	TuningAgentProjectInstructionChars = 20000
	TuningAgentWorkspacePlanItems      = 20
	TuningAgentWorkspacePlanItemChars  = 200

	TuningAgentRefTokens = 8000

	TuningAgentRecallPageChars    = 8000
	TuningAgentRecallMaxPageChars = 16000

	TuningAgentSubagentMaxSteps         = 12
	TuningAgentSubagentMaxConcurrent    = 2
	TuningAgentSubagentDigestChars      = 6000
	TuningAgentSubagentDescriptionChars = 160
	TuningAgentSubagentPromptChars      = 4000
	TuningAgentSubagentTaskChars        = 4000
	TuningAgentSubagentContextIDs       = 20

	TuningAgentWebSearchMaxUses = 5

	TuningAgentWebfetchMaxBytes       = 2 << 20
	TuningAgentWebfetchTimeoutSeconds = 10
	TuningAgentWebfetchMaxRedirects   = 5
	TuningAgentWebfetchPageChars      = 60000
	TuningAgentWebfetchTextDefault    = 12000
	TuningAgentWebfetchUserAgent      = "atrium-webfetch/1"
)
