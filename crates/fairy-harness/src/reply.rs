use fairy_domain::{
    AssistantSource, CompiledReply, CompiledReplyChain, ErrorCode, FairyError, ResponseText,
    SpeechText, VisualStateId, VisualStatePromptEntry,
};
use serde::Deserialize;

const MAX_SPEECH_CHARS: usize = 96;

#[derive(Clone, Copy, Debug, Default)]
pub struct ReplyCompiler;

impl ReplyCompiler {
    pub fn compile(
        self,
        draft: &str,
        sources: Vec<AssistantSource>,
        available_visual_states: &[VisualStatePromptEntry],
    ) -> Result<CompiledReply, FairyError> {
        validate_available_visual_states(available_visual_states)?;
        validate_draft(draft)?;
        compile_json_reply_chains(draft, sources, available_visual_states)
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct JsonReplyChains {
    chains: Vec<JsonReplyChain>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct JsonReplyChain {
    visual_state: VisualStateId,
    text: String,
}

fn compile_json_reply_chains(
    draft: &str,
    sources: Vec<AssistantSource>,
    available_visual_states: &[VisualStatePromptEntry],
) -> Result<CompiledReply, FairyError> {
    let parsed: JsonReplyChains = serde_json::from_str(draft.trim())
        .map_err(|_| invalid_reply("模型没有返回严格 reply chains JSON"))?;
    if parsed.chains.is_empty() || parsed.chains.len() > 5 {
        return Err(invalid_reply("模型 reply chains 数量必须为 1-5 段"));
    }
    let chains = parsed
        .chains
        .into_iter()
        .map(|chain| compile_chain(chain.visual_state, &chain.text, available_visual_states))
        .collect::<Result<Vec<_>, _>>()?;
    compiled_reply_from_chains(chains, sources)
}

fn compiled_reply_from_chains(
    chains: Vec<CompiledReplyChain>,
    sources: Vec<AssistantSource>,
) -> Result<CompiledReply, FairyError> {
    let display = chains
        .iter()
        .map(|chain| chain.text.as_str())
        .collect::<Vec<_>>()
        .join("\n");
    let speech_text = chains
        .first()
        .expect("reply chains are non-empty")
        .speech_text
        .clone();
    let visual_state = chains
        .last()
        .expect("reply chains are non-empty")
        .visual_state
        .clone();
    Ok(CompiledReply {
        display_text: ResponseText::new(display)?,
        speech_text,
        sources,
        visual_state,
        chains,
    })
}

fn compile_chain(
    visual_state: VisualStateId,
    raw_text: &str,
    available_visual_states: &[VisualStatePromptEntry],
) -> Result<CompiledReplyChain, FairyError> {
    if !available_visual_states
        .iter()
        .any(|state| state.id == visual_state)
    {
        return Err(invalid_reply("模型返回了当前角色包未声明的视觉状态"));
    }
    let display = sanitize_display_text(raw_text);
    if display.is_empty() {
        return Err(invalid_reply("模型没有返回可用回复文本"));
    }
    let speech = first_semantic_sentence(&display).to_owned();
    validate_speech(&speech)?;
    Ok(CompiledReplyChain {
        text: ResponseText::new(display)?,
        speech_text: SpeechText::new(speech)?,
        visual_state,
    })
}

fn sanitize_display_text(value: &str) -> String {
    value
        .lines()
        .filter_map(|line| {
            let cleaned = strip_leading_bracketed_clauses(line.trim()).trim();
            if cleaned.is_empty() || is_bracketed_clause(cleaned) {
                None
            } else {
                Some(cleaned.to_owned())
            }
        })
        .collect::<Vec<_>>()
        .join("\n")
        .trim()
        .to_owned()
}

fn strip_leading_bracketed_clauses(mut value: &str) -> &str {
    while let Some(rest) = strip_one_leading_bracketed_clause(value) {
        value = rest.trim_start();
    }
    value
}

fn strip_one_leading_bracketed_clause(value: &str) -> Option<&str> {
    let mut chars = value.char_indices();
    let (_, open) = chars.next()?;
    let close = matching_close_bracket(open)?;
    for (index, character) in chars {
        if character == close {
            return Some(&value[index + character.len_utf8()..]);
        }
    }
    None
}

fn is_bracketed_clause(value: &str) -> bool {
    let mut chars = value.chars();
    let Some(open) = chars.next() else {
        return false;
    };
    let Some(close) = matching_close_bracket(open) else {
        return false;
    };
    value.ends_with(close)
}

const fn matching_close_bracket(open: char) -> Option<char> {
    match open {
        '（' => Some('）'),
        '(' => Some(')'),
        '【' => Some('】'),
        '[' => Some(']'),
        _ => None,
    }
}

fn validate_available_visual_states(states: &[VisualStatePromptEntry]) -> Result<(), FairyError> {
    if states.is_empty() || states.len() > 16 {
        return Err(FairyError::new(
            ErrorCode::InvalidEventPayload,
            "可用视觉状态清单必须包含 1-16 个状态",
            false,
        ));
    }
    if !states.iter().any(|state| state.id.as_str() == "idle") {
        return Err(FairyError::new(
            ErrorCode::InvalidEventPayload,
            "可用视觉状态清单必须包含 idle",
            false,
        ));
    }
    for (index, state) in states.iter().enumerate() {
        if states[..index]
            .iter()
            .any(|previous| previous.id == state.id)
        {
            return Err(FairyError::new(
                ErrorCode::InvalidEventPayload,
                "可用视觉状态清单包含重复状态",
                false,
            ));
        }
        let description_length = state.description.chars().count();
        if description_length == 0
            || description_length > 96
            || state.description.trim() != state.description
            || state.description.chars().any(char::is_control)
        {
            return Err(FairyError::new(
                ErrorCode::InvalidEventPayload,
                "可用视觉状态描述无效",
                false,
            ));
        }
    }
    Ok(())
}

fn validate_draft(draft: &str) -> Result<(), FairyError> {
    if draft.is_empty() {
        return Err(invalid_reply("模型没有返回可用回复文本"));
    }
    if draft.chars().any(|character| {
        character == '\0' || (character.is_control() && !matches!(character, '\n' | '\r' | '\t'))
    }) {
        return Err(invalid_reply("模型回复包含不允许的控制字符"));
    }
    if draft.chars().any(is_emoji) {
        return Err(invalid_reply("模型回复包含不适合语音对话的 emoji"));
    }
    Ok(())
}

fn first_semantic_sentence(value: &str) -> &str {
    for (index, character) in value.char_indices() {
        if matches!(character, '。' | '！' | '？' | '!' | '?') {
            return value[..index + character.len_utf8()].trim();
        }
        if matches!(character, '\n' | '\r') {
            return value[..index].trim();
        }
    }
    value.trim()
}

fn validate_speech(value: &str) -> Result<(), FairyError> {
    if value.is_empty() {
        return Err(invalid_reply("模型回复没有可朗读台词"));
    }
    if value.chars().count() > MAX_SPEECH_CHARS {
        return Err(invalid_reply("模型回复的第一句话超过语音长度上限"));
    }
    if value.contains(['\r', '\n']) {
        return Err(invalid_reply("语音台词不能包含换行"));
    }
    let lower = value.to_ascii_lowercase();
    if lower.contains("https://") || lower.contains("http://") || lower.contains("www.") {
        return Err(invalid_reply("语音台词不能包含 URL"));
    }
    if value.contains('`')
        || value.starts_with('#')
        || value.starts_with("- ")
        || value.starts_with('*')
        || value.starts_with("> ")
    {
        return Err(invalid_reply("语音台词不能包含 Markdown 或列表标记"));
    }
    Ok(())
}

fn is_emoji(character: char) -> bool {
    matches!(
        character as u32,
        0x1F000..=0x1FAFF
            | 0x2600..=0x26FF
            | 0x2700..=0x27BF
            | 0xFE00..=0xFE0F
    )
}

fn invalid_reply(message: &'static str) -> FairyError {
    FairyError::new(ErrorCode::ModelResponseInvalid, message, false)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn state(id: &str) -> VisualStatePromptEntry {
        VisualStatePromptEntry {
            id: id.parse().expect("visual state id"),
            description: format!("{id} 状态说明"),
        }
    }

    fn states(ids: &[&str]) -> Vec<VisualStatePromptEntry> {
        ids.iter().map(|id| state(id)).collect()
    }

    fn compile(draft: &str) -> Result<CompiledReply, FairyError> {
        ReplyCompiler.compile(draft, Vec::new(), &states(&["idle"]))
    }

    fn chains_envelope(chains: serde_json::Value) -> String {
        serde_json::json!({
            "chains": chains
        })
        .to_string()
    }

    fn envelope(state: &str, body: &str) -> String {
        chains_envelope(serde_json::json!([{
            "visualState": state,
            "text": body
        }]))
    }

    #[test]
    fn reply_strips_leading_action_brackets_and_keeps_spoken_text() {
        let reply = compile(&envelope(
            "idle",
            "（轻轻歪头）哎呀，你先休息一会儿吧。后面不该显示。",
        ))
        .expect("compile reply");

        assert_eq!(
            reply.display_text.as_str(),
            "哎呀，你先休息一会儿吧。后面不该显示。"
        );
        assert_eq!(reply.speech_text.as_str(), "哎呀，你先休息一会儿吧。");
    }

    #[test]
    fn display_keeps_one_message_but_speech_is_only_first_sentence() {
        let reply = compile(&envelope(
            "idle",
            "先检查网络连接。然后确认 Base URL 和协议是否一致。",
        ))
        .expect("compile reply");

        assert_eq!(
            reply.display_text.as_str(),
            "先检查网络连接。然后确认 Base URL 和协议是否一致。"
        );
        assert_eq!(reply.speech_text.as_str(), "先检查网络连接。");
    }

    #[test]
    fn natural_sentence_final_particles_are_preserved() {
        let reply =
            compile(&envelope("idle", "那就先休息一会儿吧。")).expect("compile natural speech");
        assert_eq!(reply.speech_text.as_str(), "那就先休息一会儿吧。");
    }

    #[test]
    fn natural_fillers_are_preserved_across_the_whole_speech_sentence() {
        let reply = compile(&envelope("idle", "我，嗯，觉得你可以先休息。"))
            .expect("compile sentence with middle filler");
        assert_eq!(reply.speech_text.as_str(), "我，嗯，觉得你可以先休息。");

        let leading = compile(&envelope("idle", "唔，我觉得你可以先休息一下。"))
            .expect("compile sentence with leading filler");
        assert_eq!(leading.speech_text.as_str(), "唔，我觉得你可以先休息一下。");

        let semantic =
            compile(&envelope("idle", "嗯哼，这次我听懂了。")).expect("keep semantic expression");
        assert_eq!(semantic.speech_text.as_str(), "嗯哼，这次我听懂了。");
    }

    #[test]
    fn filler_only_reply_is_valid_spoken_dialogue() {
        let reply =
            compile(&envelope("idle", "嗯。")).expect("a filler can be a complete human reply");
        assert_eq!(reply.display_text.as_str(), "嗯。");
        assert_eq!(reply.speech_text.as_str(), "嗯。");
    }

    #[test]
    fn reply_strips_standalone_actions_but_preserves_inline_brackets() {
        let reply = compile(&envelope(
            "idle",
            "先检查网络。\n（轻轻点头）\n然后检查配置。",
        ))
        .expect("strip standalone stage line");
        assert_eq!(reply.display_text.as_str(), "先检查网络。\n然后检查配置。");
        assert_eq!(reply.speech_text.as_str(), "先检查网络。");

        let inline = compile(&envelope("idle", "先检查网络。然后确认配置（不要猜测）。"))
            .expect("inline brackets remain valid");
        assert_eq!(
            inline.display_text.as_str(),
            "先检查网络。然后确认配置（不要猜测）。"
        );

        let psychological = compile(&envelope(
            "idle",
            "我听见了。\n（心里有些担心）\n你愿意再说一点吗？",
        ))
        .expect("strip standalone psychological line");
        assert_eq!(
            psychological.display_text.as_str(),
            "我听见了。\n你愿意再说一点吗？"
        );
    }

    #[test]
    fn pure_bracketed_action_and_other_invalid_speech_fail() {
        assert!(compile(&envelope("idle", "（安静地看着你）")).is_err());
        for invalid in [
            "看看 https://example.test。",
            "**加粗台词**",
            "我在这里🙂。",
        ] {
            assert!(compile(&envelope("idle", invalid)).is_err());
        }
    }

    #[test]
    fn short_unpunctuated_line_is_still_speakable() {
        let reply = compile(&envelope("idle", "我在")).expect("compile short line");
        assert_eq!(reply.speech_text.as_str(), "我在");
    }

    #[test]
    fn chains_envelope_visual_state_is_validated_against_available_states() {
        let reply = ReplyCompiler
            .compile(
                &envelope("happy", "好呀，我也觉得这个方向不错。"),
                Vec::new(),
                &states(&["idle", "happy", "sad"]),
            )
            .expect("compile visual state");

        assert_eq!(reply.visual_state.as_str(), "happy");
        assert_eq!(reply.display_text.as_str(), "好呀，我也觉得这个方向不错。");
        assert_eq!(reply.speech_text.as_str(), "好呀，我也觉得这个方向不错。");
        assert_eq!(reply.chains.len(), 1);
        assert_eq!(reply.chains[0].visual_state.as_str(), "happy");
    }

    #[test]
    fn json_reply_chains_compile_to_aggregate_fields() {
        let draft = chains_envelope(serde_json::json!([
            {"visualState": "thinking", "text": "嗯，我懂。"},
            {"visualState": "happy", "text": "先这样改。"}
        ]));
        let reply = ReplyCompiler
            .compile(&draft, Vec::new(), &states(&["idle", "thinking", "happy"]))
            .expect("compile json chains");

        assert_eq!(reply.chains.len(), 2);
        assert_eq!(reply.chains[0].visual_state.as_str(), "thinking");
        assert_eq!(reply.chains[1].visual_state.as_str(), "happy");
        assert_eq!(reply.display_text.as_str(), "嗯，我懂。\n先这样改。");
        assert_eq!(reply.speech_text.as_str(), "嗯，我懂。");
        assert_eq!(reply.visual_state.as_str(), "happy");
    }

    #[test]
    fn json_reply_chains_do_not_semantically_reject_character_catchphrases() {
        let draft = envelope("happy", "我是高性能的嘛！不过今天先慢慢来。");
        let reply = ReplyCompiler
            .compile(&draft, Vec::new(), &states(&["idle", "happy"]))
            .expect("compile catchphrase as ordinary model text");

        assert_eq!(
            reply.display_text.as_str(),
            "我是高性能的嘛！不过今天先慢慢来。"
        );
        assert_eq!(reply.speech_text.as_str(), "我是高性能的嘛！");
        assert_eq!(reply.visual_state.as_str(), "happy");
    }

    #[test]
    fn json_reply_chains_reject_undeclared_state_and_bad_counts() {
        let undeclared_draft = envelope("angry", "我在。");
        let undeclared = ReplyCompiler
            .compile(&undeclared_draft, Vec::new(), &states(&["idle", "happy"]))
            .expect_err("undeclared state fails");
        assert_eq!(undeclared.code, ErrorCode::ModelResponseInvalid);

        let empty_draft = chains_envelope(serde_json::json!([]));
        let empty = ReplyCompiler
            .compile(&empty_draft, Vec::new(), &states(&["idle"]))
            .expect_err("empty chains fail");
        assert_eq!(empty.code, ErrorCode::ModelResponseInvalid);

        let too_many_draft = chains_envelope(serde_json::json!([
            {"visualState": "idle", "text": "1"},
            {"visualState": "idle", "text": "2"},
            {"visualState": "idle", "text": "3"},
            {"visualState": "idle", "text": "4"},
            {"visualState": "idle", "text": "5"},
            {"visualState": "idle", "text": "6"}
        ]));
        let too_many = ReplyCompiler
            .compile(&too_many_draft, Vec::new(), &states(&["idle"]))
            .expect_err("too many chains fail");
        assert_eq!(too_many.code, ErrorCode::ModelResponseInvalid);
    }

    #[test]
    fn legacy_or_undeclared_visual_state_fails() {
        for invalid in [
            "（开心地蹦跳）在的在的！".to_owned(),
            "VISUAL_STATE: idle\n我在。".to_owned(),
            envelope("angry", "我在。"),
            envelope("Bad", "我在。"),
        ] {
            let error = ReplyCompiler
                .compile(&invalid, Vec::new(), &states(&["idle", "happy"]))
                .expect_err("invalid visual state fails");
            assert_eq!(error.code, ErrorCode::ModelResponseInvalid);
        }
    }

    #[test]
    fn invalid_available_state_context_fails_before_model_text_is_used() {
        let error = ReplyCompiler
            .compile(&envelope("idle", "我在。"), Vec::new(), &[])
            .expect_err("empty available states fail");
        assert_eq!(error.code, ErrorCode::InvalidEventPayload);
    }

    #[test]
    fn strict_chains_envelope_compiles() {
        let reply = compile(r#"{"chains":[{"visualState":"idle","text":"我在。"}]}"#)
            .expect("compile strict chains envelope");
        assert_eq!(reply.display_text.as_str(), "我在。");
        assert_eq!(reply.speech_text.as_str(), "我在。");
        assert_eq!(reply.visual_state.as_str(), "idle");
    }

    #[test]
    fn strict_chains_envelope_rejects_unknown_trailing_and_legacy_outputs() {
        let valid_chains = r#"[{"visualState":"idle","text":"我在。"}]"#;
        let valid = format!(r#"{{"chains":{valid_chains}}}"#);
        let cases = vec![
            ("missing chains", "{}".to_owned()),
            (
                "unknown top field",
                format!(r#"{{"chains":{valid_chains},"reasoning":"no"}}"#),
            ),
            (
                "decision field is not part of the wire schema",
                format!(r#"{{"decision":{{"stance":"先回应"}},"chains":{valid_chains}}}"#),
            ),
            (
                "unknown chain field",
                r#"{"chains":[{"visualState":"idle","text":"我在。","gesture":"wave"}]}"#
                    .to_string(),
            ),
            ("second json value", format!("{valid}{{}}")),
            ("trailing text", format!("{valid} trailing")),
            (
                "legacy visual header",
                "VISUAL_STATE: idle\n我在。".to_owned(),
            ),
            ("legacy plain text", "我在。".to_owned()),
        ];

        for (name, draft) in cases {
            let error = compile(&draft).expect_err(name);
            assert_eq!(error.code, ErrorCode::ModelResponseInvalid, "{name}");
        }
    }
}
