package mail

const (
	TuningBirthdaysDefaultDays      = 14
	TuningBirthdaysMaxDays          = 366
	TuningDeletionIntervalSeconds   = 86400
	TuningDeletionsMinDeletedShare  = 0.7
	TuningDeletionsMinGroup         = 5
	TuningDeletionsOtpMinShare      = 0.8
	TuningDeletionsOtpOlderThanDays = 7
	TuningDraftsMaxBody             = 100000
	TuningDraftsMaxSubject          = 300
	TuningFolderMinMessages         = 10
	TuningKnnK                      = 7
	TuningKnnMax                    = 4096
	TuningKnnMinShare               = 0.9
	TuningLlmNeighbors              = 10
	TuningMailBackoffMaxSeconds     = 300
	TuningMailBackoffStartSeconds   = 5
	TuningMailBodyCapBytes          = 65536
	TuningMailFetchBatch            = 40
	TuningMailFlagBatch             = 500
	TuningMailFtsBodyChars          = 20000
	TuningMailIdleSettleSeconds     = 2
	TuningMailScanSampleSubjects    = 5
	TuningMailScanTopLabels         = 5
	TuningMailSearchBranchLimit     = 50
	TuningMailSearchMaxTerms        = 12
	TuningMailSearchRrfK            = 60
	TuningMailSearchSnippetChars    = 220
	TuningMailSearchSnippetTokens   = 24
	TuningMailopsBatch              = 100
	TuningReportDisagreementLines   = 40
	TuningReportTopDests            = 15
	TuningRuleMinN                  = 3
	TuningRuleMinPurity             = 0.9
	TuningSentBackfillDays          = 90
	TuningTriageTopDests            = 15
	TuningWriteAttempts             = 3
)
