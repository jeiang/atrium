// Code generated from the frozen Python app; not regenerable. DO NOT EDIT.

package loop

var blockRoots = []string{"BetaTextBlock", "BetaThinkingBlock", "BetaRedactedThinkingBlock", "BetaToolUseBlock", "BetaServerToolUseBlock", "BetaWebSearchToolResultBlock", "BetaWebFetchToolResultBlock", "BetaAdvisorToolResultBlock", "BetaCodeExecutionToolResultBlock", "BetaBashCodeExecutionToolResultBlock", "BetaTextEditorCodeExecutionToolResultBlock", "BetaToolSearchToolResultBlock", "BetaMCPToolUseBlock", "BetaMCPToolResultBlock", "BetaContainerUploadBlock", "BetaCompactionBlock", "BetaFallbackBlock"}

var blockModels = map[string]blockModel{
	"BetaAdvisorRedactedResultBlock": {types: []string{"advisor_redacted_result"}, fields: []blockField{
		{"encrypted_content", nil},
		{"stop_reason", nil},
		{"type", nil},
	}},
	"BetaAdvisorResultBlock": {types: []string{"advisor_result"}, fields: []blockField{
		{"stop_reason", nil},
		{"text", nil},
		{"type", nil},
	}},
	"BetaAdvisorToolResultBlock": {types: []string{"advisor_tool_result"}, fields: []blockField{
		{"content", []string{"BetaAdvisorToolResultError", "BetaAdvisorResultBlock", "BetaAdvisorRedactedResultBlock"}},
		{"tool_use_id", nil},
		{"type", nil},
	}},
	"BetaAdvisorToolResultError": {types: []string{"advisor_tool_result_error"}, fields: []blockField{
		{"error_code", nil},
		{"type", nil},
	}},
	"BetaBase64PDFSource": {types: []string{"base64"}, fields: []blockField{
		{"data", nil},
		{"media_type", nil},
		{"type", nil},
	}},
	"BetaBashCodeExecutionOutputBlock": {types: []string{"bash_code_execution_output"}, fields: []blockField{
		{"file_id", nil},
		{"type", nil},
	}},
	"BetaBashCodeExecutionResultBlock": {types: []string{"bash_code_execution_result"}, fields: []blockField{
		{"content", []string{"BetaBashCodeExecutionOutputBlock"}},
		{"return_code", nil},
		{"stderr", nil},
		{"stdout", nil},
		{"type", nil},
	}},
	"BetaBashCodeExecutionToolResultBlock": {types: []string{"bash_code_execution_tool_result"}, fields: []blockField{
		{"content", []string{"BetaBashCodeExecutionToolResultError", "BetaBashCodeExecutionResultBlock"}},
		{"tool_use_id", nil},
		{"type", nil},
	}},
	"BetaBashCodeExecutionToolResultError": {types: []string{"bash_code_execution_tool_result_error"}, fields: []blockField{
		{"error_code", nil},
		{"type", nil},
	}},
	"BetaCitationCharLocation": {types: []string{"char_location"}, fields: []blockField{
		{"cited_text", nil},
		{"document_index", nil},
		{"document_title", nil},
		{"end_char_index", nil},
		{"file_id", nil},
		{"start_char_index", nil},
		{"type", nil},
	}},
	"BetaCitationConfig": {types: nil, fields: []blockField{
		{"enabled", nil},
	}},
	"BetaCitationContentBlockLocation": {types: []string{"content_block_location"}, fields: []blockField{
		{"cited_text", nil},
		{"document_index", nil},
		{"document_title", nil},
		{"end_block_index", nil},
		{"file_id", nil},
		{"start_block_index", nil},
		{"type", nil},
	}},
	"BetaCitationPageLocation": {types: []string{"page_location"}, fields: []blockField{
		{"cited_text", nil},
		{"document_index", nil},
		{"document_title", nil},
		{"end_page_number", nil},
		{"file_id", nil},
		{"start_page_number", nil},
		{"type", nil},
	}},
	"BetaCitationSearchResultLocation": {types: []string{"search_result_location"}, fields: []blockField{
		{"cited_text", nil},
		{"end_block_index", nil},
		{"search_result_index", nil},
		{"source", nil},
		{"start_block_index", nil},
		{"title", nil},
		{"type", nil},
	}},
	"BetaCitationsWebSearchResultLocation": {types: []string{"web_search_result_location"}, fields: []blockField{
		{"cited_text", nil},
		{"encrypted_index", nil},
		{"title", nil},
		{"type", nil},
		{"url", nil},
	}},
	"BetaCodeExecutionOutputBlock": {types: []string{"code_execution_output"}, fields: []blockField{
		{"file_id", nil},
		{"type", nil},
	}},
	"BetaCodeExecutionResultBlock": {types: []string{"code_execution_result"}, fields: []blockField{
		{"content", []string{"BetaCodeExecutionOutputBlock"}},
		{"return_code", nil},
		{"stderr", nil},
		{"stdout", nil},
		{"type", nil},
	}},
	"BetaCodeExecutionToolResultBlock": {types: []string{"code_execution_tool_result"}, fields: []blockField{
		{"content", []string{"BetaCodeExecutionToolResultError", "BetaCodeExecutionResultBlock", "BetaEncryptedCodeExecutionResultBlock"}},
		{"tool_use_id", nil},
		{"type", nil},
	}},
	"BetaCodeExecutionToolResultError": {types: []string{"code_execution_tool_result_error"}, fields: []blockField{
		{"error_code", nil},
		{"type", nil},
	}},
	"BetaCompactionBlock": {types: []string{"compaction"}, fields: []blockField{
		{"content", nil},
		{"encrypted_content", nil},
		{"type", nil},
		{"signature", nil},
	}},
	"BetaContainerUploadBlock": {types: []string{"container_upload"}, fields: []blockField{
		{"file_id", nil},
		{"type", nil},
	}},
	"BetaDirectCaller": {types: []string{"direct"}, fields: []blockField{
		{"type", nil},
	}},
	"BetaDocumentBlock": {types: []string{"document"}, fields: []blockField{
		{"citations", []string{"BetaCitationConfig"}},
		{"source", []string{"BetaBase64PDFSource", "BetaPlainTextSource"}},
		{"title", nil},
		{"type", nil},
	}},
	"BetaEncryptedCodeExecutionResultBlock": {types: []string{"encrypted_code_execution_result"}, fields: []blockField{
		{"content", []string{"BetaCodeExecutionOutputBlock"}},
		{"encrypted_stdout", nil},
		{"return_code", nil},
		{"stderr", nil},
		{"type", nil},
	}},
	"BetaFallbackBlock": {types: []string{"fallback"}, fields: []blockField{
		{"from", []string{"BetaFallbackInfo"}},
		{"to", []string{"BetaFallbackInfo"}},
		{"trigger", []string{"BetaFallbackRefusalTrigger"}},
		{"type", nil},
	}},
	"BetaFallbackInfo": {types: nil, fields: []blockField{
		{"model", nil},
	}},
	"BetaFallbackRefusalTrigger": {types: []string{"refusal"}, fields: []blockField{
		{"category", nil},
		{"type", nil},
	}},
	"BetaMCPToolResultBlock": {types: []string{"mcp_tool_result"}, fields: []blockField{
		{"content", []string{"BetaTextBlock"}},
		{"is_error", nil},
		{"tool_use_id", nil},
		{"type", nil},
	}},
	"BetaMCPToolUseBlock": {types: []string{"mcp_tool_use"}, fields: []blockField{
		{"id", nil},
		{"input", nil},
		{"name", nil},
		{"server_name", nil},
		{"type", nil},
	}},
	"BetaPlainTextSource": {types: []string{"text"}, fields: []blockField{
		{"data", nil},
		{"media_type", nil},
		{"type", nil},
	}},
	"BetaRedactedThinkingBlock": {types: []string{"redacted_thinking"}, fields: []blockField{
		{"data", nil},
		{"type", nil},
	}},
	"BetaServerToolCaller": {types: []string{"code_execution_20250825"}, fields: []blockField{
		{"tool_id", nil},
		{"type", nil},
	}},
	"BetaServerToolCaller20260120": {types: []string{"code_execution_20260120"}, fields: []blockField{
		{"tool_id", nil},
		{"type", nil},
	}},
	"BetaServerToolUseBlock": {types: []string{"server_tool_use"}, fields: []blockField{
		{"id", nil},
		{"input", nil},
		{"name", nil},
		{"type", nil},
		{"caller", []string{"BetaDirectCaller", "BetaServerToolCaller", "BetaServerToolCaller20260120"}},
	}},
	"BetaTextBlock": {types: []string{"text"}, fields: []blockField{
		{"citations", []string{"BetaCitationCharLocation", "BetaCitationPageLocation", "BetaCitationContentBlockLocation", "BetaCitationsWebSearchResultLocation", "BetaCitationSearchResultLocation"}},
		{"text", nil},
		{"type", nil},
	}},
	"BetaTextEditorCodeExecutionCreateResultBlock": {types: []string{"text_editor_code_execution_create_result"}, fields: []blockField{
		{"is_file_update", nil},
		{"type", nil},
	}},
	"BetaTextEditorCodeExecutionStrReplaceResultBlock": {types: []string{"text_editor_code_execution_str_replace_result"}, fields: []blockField{
		{"lines", nil},
		{"new_lines", nil},
		{"new_start", nil},
		{"old_lines", nil},
		{"old_start", nil},
		{"type", nil},
	}},
	"BetaTextEditorCodeExecutionToolResultBlock": {types: []string{"text_editor_code_execution_tool_result"}, fields: []blockField{
		{"content", []string{"BetaTextEditorCodeExecutionToolResultError", "BetaTextEditorCodeExecutionViewResultBlock", "BetaTextEditorCodeExecutionCreateResultBlock", "BetaTextEditorCodeExecutionStrReplaceResultBlock"}},
		{"tool_use_id", nil},
		{"type", nil},
	}},
	"BetaTextEditorCodeExecutionToolResultError": {types: []string{"text_editor_code_execution_tool_result_error"}, fields: []blockField{
		{"error_code", nil},
		{"error_message", nil},
		{"type", nil},
	}},
	"BetaTextEditorCodeExecutionViewResultBlock": {types: []string{"text_editor_code_execution_view_result"}, fields: []blockField{
		{"content", nil},
		{"file_type", nil},
		{"num_lines", nil},
		{"start_line", nil},
		{"total_lines", nil},
		{"type", nil},
	}},
	"BetaThinkingBlock": {types: []string{"thinking"}, fields: []blockField{
		{"signature", nil},
		{"thinking", nil},
		{"type", nil},
	}},
	"BetaToolReferenceBlock": {types: []string{"tool_reference"}, fields: []blockField{
		{"tool_name", nil},
		{"type", nil},
	}},
	"BetaToolSearchToolResultBlock": {types: []string{"tool_search_tool_result"}, fields: []blockField{
		{"content", []string{"BetaToolSearchToolResultError", "BetaToolSearchToolSearchResultBlock"}},
		{"tool_use_id", nil},
		{"type", nil},
	}},
	"BetaToolSearchToolResultError": {types: []string{"tool_search_tool_result_error"}, fields: []blockField{
		{"error_code", nil},
		{"error_message", nil},
		{"type", nil},
	}},
	"BetaToolSearchToolSearchResultBlock": {types: []string{"tool_search_tool_search_result"}, fields: []blockField{
		{"tool_references", []string{"BetaToolReferenceBlock"}},
		{"type", nil},
	}},
	"BetaToolUseBlock": {types: []string{"tool_use"}, fields: []blockField{
		{"id", nil},
		{"input", nil},
		{"name", nil},
		{"type", nil},
		{"caller", []string{"BetaDirectCaller", "BetaServerToolCaller", "BetaServerToolCaller20260120"}},
		{"toolset_name", nil},
	}},
	"BetaWebFetchBlock": {types: []string{"web_fetch_result"}, fields: []blockField{
		{"content", []string{"BetaDocumentBlock"}},
		{"retrieved_at", nil},
		{"type", nil},
		{"url", nil},
	}},
	"BetaWebFetchToolResultBlock": {types: []string{"web_fetch_tool_result"}, fields: []blockField{
		{"content", []string{"BetaWebFetchToolResultErrorBlock", "BetaWebFetchBlock"}},
		{"tool_use_id", nil},
		{"type", nil},
		{"caller", []string{"BetaDirectCaller", "BetaServerToolCaller", "BetaServerToolCaller20260120"}},
	}},
	"BetaWebFetchToolResultErrorBlock": {types: []string{"web_fetch_tool_result_error"}, fields: []blockField{
		{"error_code", nil},
		{"type", nil},
	}},
	"BetaWebSearchResultBlock": {types: []string{"web_search_result"}, fields: []blockField{
		{"encrypted_content", nil},
		{"page_age", nil},
		{"title", nil},
		{"type", nil},
		{"url", nil},
	}},
	"BetaWebSearchToolResultBlock": {types: []string{"web_search_tool_result"}, fields: []blockField{
		{"content", []string{"BetaWebSearchToolResultError", "BetaWebSearchResultBlock"}},
		{"tool_use_id", nil},
		{"type", nil},
		{"caller", []string{"BetaDirectCaller", "BetaServerToolCaller", "BetaServerToolCaller20260120"}},
	}},
	"BetaWebSearchToolResultError": {types: []string{"web_search_tool_result_error"}, fields: []blockField{
		{"error_code", nil},
		{"type", nil},
	}},
}
