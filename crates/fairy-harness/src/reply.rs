use fairy_domain::{
    AssistantSource, CompiledReply, ErrorCode, FairyError, ReplyMode, ResponseText, SpeechText,
};

pub const BRIEF_OUTPUT_TOKENS: u32 = 160;
pub const EXPANDED_OUTPUT_TOKENS: u32 = 640;
const MAX_SPEECH_CHARS: usize = 96;

const EXPANDED_MARKERS: [&str; 12] = [
    "详细解释",
    "详细说明",
    "详细分析",
    "深入分析",
    "展开说说",
    "展开讲讲",
    "完整说明",
    "完整解释",
    "分步骤",
    "列出步骤",
    "具体步骤",
    "大量说明",
];

const LEADING_FILLERS: [&str; 8] = ["哎呀", "嗯", "呃", "唔", "诶", "欸", "额", "啊"];

#[derive(Clone, Copy, Debug, Default)]
pub struct ReplyBudgetSelector;

impl ReplyBudgetSelector {
    #[must_use]
    pub fn select(self, input: &str) -> ReplyMode {
        if EXPANDED_MARKERS.iter().any(|marker| input.contains(marker)) {
            ReplyMode::Expanded
        } else {
            ReplyMode::Brief
        }
    }

    #[must_use]
    pub const fn output_tokens(mode: ReplyMode) -> u32 {
        match mode {
            ReplyMode::Brief => BRIEF_OUTPUT_TOKENS,
            ReplyMode::Expanded => EXPANDED_OUTPUT_TOKENS,
        }
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct ReplyCompiler;

impl ReplyCompiler {
    pub fn compile(
        self,
        mode: ReplyMode,
        draft: &str,
        sources: Vec<AssistantSource>,
    ) -> Result<CompiledReply, FairyError> {
        validate_draft(draft)?;
        let normalized = normalize_leading(draft);
        if normalized.is_empty() {
            return Err(invalid_reply("模型回复只包含动作、心理描写或填充词"));
        }
        let raw_speech = first_semantic_sentence(normalized);
        let speech = normalize_standalone_fillers(raw_speech);
        validate_speech(&speech)?;
        let speech_text = SpeechText::new(speech.clone())?;
        let display = match mode {
            ReplyMode::Brief => speech,
            ReplyMode::Expanded => {
                let rest = &normalized[raw_speech.len()..];
                normalize_expanded_display(&format!("{speech}{rest}"))?
            }
        };

        Ok(CompiledReply {
            display_text: ResponseText::new(display)?,
            speech_text,
            sources,
        })
    }
}

fn normalize_standalone_fillers(value: &str) -> String {
    let characters: Vec<char> = value.chars().collect();
    let mut output: Vec<char> = Vec::with_capacity(characters.len());
    let mut index = 0;

    while index < characters.len() {
        let matched = LEADING_FILLERS.iter().find_map(|filler| {
            let filler_chars: Vec<char> = filler.chars().collect();
            let end = index.checked_add(filler_chars.len())?;
            if end > characters.len() || characters[index..end] != filler_chars {
                return None;
            }
            let left_boundary = index == 0 || is_filler_boundary(characters[index - 1]);
            let right_boundary = end == characters.len() || is_filler_boundary(characters[end]);
            (left_boundary && right_boundary).then_some(end)
        });

        let Some(mut next) = matched else {
            output.push(characters[index]);
            index += 1;
            continue;
        };

        while next < characters.len()
            && (characters[next].is_whitespace() || is_filler_separator(characters[next]))
        {
            next += 1;
        }
        if next == characters.len() {
            while output.last().is_some_and(|character| {
                character.is_whitespace() || is_dangling_separator(*character)
            }) {
                output.pop();
            }
        }
        index = next;
    }

    output.into_iter().collect::<String>().trim().to_owned()
}

fn is_filler_boundary(character: char) -> bool {
    character.is_whitespace() || !character.is_alphanumeric()
}

fn is_filler_separator(character: char) -> bool {
    matches!(
        character,
        '，' | ','
            | '。'
            | '.'
            | '！'
            | '!'
            | '？'
            | '?'
            | '、'
            | '；'
            | ';'
            | '：'
            | ':'
            | '…'
            | '~'
            | '～'
            | '—'
    )
}

fn is_dangling_separator(character: char) -> bool {
    matches!(
        character,
        '，' | ',' | '、' | '；' | ';' | '：' | ':' | '…' | '~' | '～' | '—'
    )
}

fn normalize_expanded_display(value: &str) -> Result<String, FairyError> {
    let mut lines = Vec::new();
    for line in value.lines() {
        let trimmed = line.trim();
        if is_standalone_stage_block(trimmed) {
            continue;
        }
        if contains_bracket(trimmed) {
            return Err(invalid_reply("详细回复包含含糊的内联括号内容"));
        }
        lines.push(line.trim_end());
    }
    let display = lines.join("\n").trim().to_owned();
    if display.is_empty() {
        return Err(invalid_reply("模型回复只包含动作或心理描写"));
    }
    Ok(display)
}

fn is_standalone_stage_block(value: &str) -> bool {
    if value.len() < 2 {
        return false;
    }
    [
        ('（', '）'),
        ('(', ')'),
        ('【', '】'),
        ('[', ']'),
        ('*', '*'),
    ]
    .iter()
    .any(|(open, close)| value.starts_with(*open) && value.ends_with(*close))
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

fn normalize_leading(mut value: &str) -> &str {
    value = value.trim_start_matches(|character: char| character.is_whitespace());
    loop {
        let before = value;
        value = strip_leading_block(value).unwrap_or(value);
        value = value.trim_start_matches(|character: char| character.is_whitespace());
        value = strip_leading_filler(value);
        value = value.trim_start_matches(|character: char| character.is_whitespace());
        if value == before {
            return value;
        }
    }
}

fn strip_leading_block(value: &str) -> Option<&str> {
    let (open, close) = if value.starts_with('（') {
        ('（', '）')
    } else if value.starts_with('(') {
        ('(', ')')
    } else if value.starts_with('【') {
        ('【', '】')
    } else if value.starts_with('[') {
        ('[', ']')
    } else if value.starts_with("**") {
        return None;
    } else if value.starts_with('*') {
        ('*', '*')
    } else {
        return None;
    };
    let rest = &value[open.len_utf8()..];
    let end = rest.find(close)?;
    Some(&rest[end + close.len_utf8()..])
}

fn strip_leading_filler(value: &str) -> &str {
    for filler in LEADING_FILLERS {
        let Some(rest) = value.strip_prefix(filler) else {
            continue;
        };
        if rest
            .chars()
            .next()
            .is_some_and(|character| !is_filler_boundary(character))
        {
            continue;
        }
        return rest.trim_start_matches(['，', ',', '、', '。', '！', '!', '…', '~', '～']);
    }
    value
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
    if contains_bracket(value) {
        return Err(invalid_reply("语音台词不能包含括号动作或引用"));
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

fn contains_bracket(value: &str) -> bool {
    ['（', '）', '(', ')', '【', '】', '[', ']']
        .iter()
        .any(|bracket| value.contains(*bracket))
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

    #[test]
    fn reply_mode_requires_an_explicit_long_form_marker() {
        let selector = ReplyBudgetSelector;
        assert_eq!(selector.select("为什么会这样？"), ReplyMode::Brief);
        assert_eq!(selector.select("现在几点？"), ReplyMode::Brief);
        assert_eq!(
            selector.select("请详细分析为什么会这样"),
            ReplyMode::Expanded
        );
        assert_eq!(selector.select("分步骤告诉我怎么做"), ReplyMode::Expanded);
        assert_eq!(ReplyBudgetSelector::output_tokens(ReplyMode::Brief), 160);
        assert_eq!(ReplyBudgetSelector::output_tokens(ReplyMode::Expanded), 640);
    }

    #[test]
    fn brief_reply_removes_leading_stage_direction_and_filler() {
        let reply = ReplyCompiler
            .compile(
                ReplyMode::Brief,
                "（轻轻歪头）哎呀，你先休息一会儿吧。后面不该显示。",
                Vec::new(),
            )
            .expect("compile brief reply");

        assert_eq!(reply.display_text.as_str(), "你先休息一会儿吧。");
        assert_eq!(reply.speech_text.as_str(), "你先休息一会儿吧。");
    }

    #[test]
    fn expanded_reply_keeps_one_message_but_speech_is_only_first_sentence() {
        let reply = ReplyCompiler
            .compile(
                ReplyMode::Expanded,
                "先检查网络连接。然后确认 Base URL 和协议是否一致。",
                Vec::new(),
            )
            .expect("compile expanded reply");

        assert_eq!(
            reply.display_text.as_str(),
            "先检查网络连接。然后确认 Base URL 和协议是否一致。"
        );
        assert_eq!(reply.speech_text.as_str(), "先检查网络连接。");
    }

    #[test]
    fn natural_sentence_final_particles_are_preserved() {
        let reply = ReplyCompiler
            .compile(ReplyMode::Brief, "那就先休息一会儿吧。", Vec::new())
            .expect("compile natural speech");
        assert_eq!(reply.speech_text.as_str(), "那就先休息一会儿吧。");
    }

    #[test]
    fn standalone_fillers_are_removed_across_the_whole_speech_sentence() {
        let reply = ReplyCompiler
            .compile(ReplyMode::Brief, "我，嗯，觉得你可以先休息。", Vec::new())
            .expect("compile sentence with middle filler");
        assert_eq!(reply.speech_text.as_str(), "我，觉得你可以先休息。");

        let semantic = ReplyCompiler
            .compile(ReplyMode::Brief, "嗯哼，这次我听懂了。", Vec::new())
            .expect("keep semantic expression");
        assert_eq!(semantic.speech_text.as_str(), "嗯哼，这次我听懂了。");
    }

    #[test]
    fn filler_only_draft_fails_without_a_template() {
        let error = ReplyCompiler
            .compile(ReplyMode::Brief, "嗯，呃……", Vec::new())
            .expect_err("filler-only draft must fail");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);
    }

    #[test]
    fn expanded_reply_removes_standalone_stage_lines_and_rejects_inline_brackets() {
        let reply = ReplyCompiler
            .compile(
                ReplyMode::Expanded,
                "先检查网络。\n（轻轻点头）\n然后检查配置。",
                Vec::new(),
            )
            .expect("remove standalone stage line");
        assert_eq!(reply.display_text.as_str(), "先检查网络。\n然后检查配置。");
        assert_eq!(reply.speech_text.as_str(), "先检查网络。");

        let error = ReplyCompiler
            .compile(
                ReplyMode::Expanded,
                "先检查网络。然后确认配置（不要猜测）。",
                Vec::new(),
            )
            .expect_err("ambiguous inline brackets must fail");
        assert_eq!(error.code, ErrorCode::ModelResponseInvalid);
    }

    #[test]
    fn pure_stage_direction_and_invalid_speech_fail_without_template() {
        assert!(
            ReplyCompiler
                .compile(ReplyMode::Brief, "（安静地看着你）", Vec::new())
                .is_err()
        );
        for invalid in [
            "看看 https://example.test。",
            "**加粗台词**",
            "我在这里🙂。",
            "这句话包含（心理活动）。",
        ] {
            assert!(
                ReplyCompiler
                    .compile(ReplyMode::Brief, invalid, Vec::new())
                    .is_err()
            );
        }
    }

    #[test]
    fn short_unpunctuated_line_is_still_speakable() {
        let reply = ReplyCompiler
            .compile(ReplyMode::Brief, "我在", Vec::new())
            .expect("compile short line");
        assert_eq!(reply.speech_text.as_str(), "我在");
    }
}
