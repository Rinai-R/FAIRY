use fairy_domain::{
    AssistantSource, CompiledReply, ErrorCode, FairyError, ResponseText, SpeechText,
};

const MAX_SPEECH_CHARS: usize = 96;

#[derive(Clone, Copy, Debug, Default)]
pub struct ReplyCompiler;

impl ReplyCompiler {
    pub fn compile(
        self,
        draft: &str,
        sources: Vec<AssistantSource>,
    ) -> Result<CompiledReply, FairyError> {
        validate_draft(draft)?;
        let display = draft.trim().to_owned();
        if display.is_empty() {
            return Err(invalid_reply("模型没有返回可用回复文本"));
        }
        let raw_speech = first_semantic_sentence(&display);
        let speech = raw_speech.to_owned();
        validate_speech(&speech)?;
        let speech_text = SpeechText::new(speech.clone())?;

        Ok(CompiledReply {
            display_text: ResponseText::new(display)?,
            speech_text,
            sources,
        })
    }
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

    #[test]
    fn reply_preserves_leading_brackets_and_spoken_filler() {
        let reply = ReplyCompiler
            .compile(
                "（轻轻歪头）哎呀，你先休息一会儿吧。后面不该显示。",
                Vec::new(),
            )
            .expect("compile reply");

        assert_eq!(
            reply.display_text.as_str(),
            "（轻轻歪头）哎呀，你先休息一会儿吧。后面不该显示。"
        );
        assert_eq!(
            reply.speech_text.as_str(),
            "（轻轻歪头）哎呀，你先休息一会儿吧。"
        );
    }

    #[test]
    fn display_keeps_one_message_but_speech_is_only_first_sentence() {
        let reply = ReplyCompiler
            .compile(
                "先检查网络连接。然后确认 Base URL 和协议是否一致。",
                Vec::new(),
            )
            .expect("compile reply");

        assert_eq!(
            reply.display_text.as_str(),
            "先检查网络连接。然后确认 Base URL 和协议是否一致。"
        );
        assert_eq!(reply.speech_text.as_str(), "先检查网络连接。");
    }

    #[test]
    fn natural_sentence_final_particles_are_preserved() {
        let reply = ReplyCompiler
            .compile("那就先休息一会儿吧。", Vec::new())
            .expect("compile natural speech");
        assert_eq!(reply.speech_text.as_str(), "那就先休息一会儿吧。");
    }

    #[test]
    fn natural_fillers_are_preserved_across_the_whole_speech_sentence() {
        let reply = ReplyCompiler
            .compile("我，嗯，觉得你可以先休息。", Vec::new())
            .expect("compile sentence with middle filler");
        assert_eq!(reply.speech_text.as_str(), "我，嗯，觉得你可以先休息。");

        let leading = ReplyCompiler
            .compile("唔，我觉得你可以先休息一下。", Vec::new())
            .expect("compile sentence with leading filler");
        assert_eq!(leading.speech_text.as_str(), "唔，我觉得你可以先休息一下。");

        let semantic = ReplyCompiler
            .compile("嗯哼，这次我听懂了。", Vec::new())
            .expect("keep semantic expression");
        assert_eq!(semantic.speech_text.as_str(), "嗯哼，这次我听懂了。");
    }

    #[test]
    fn filler_only_reply_is_valid_spoken_dialogue() {
        let reply = ReplyCompiler
            .compile("嗯。", Vec::new())
            .expect("a filler can be a complete human reply");
        assert_eq!(reply.display_text.as_str(), "嗯。");
        assert_eq!(reply.speech_text.as_str(), "嗯。");
    }

    #[test]
    fn reply_preserves_standalone_and_inline_brackets() {
        let reply = ReplyCompiler
            .compile("先检查网络。\n（轻轻点头）\n然后检查配置。", Vec::new())
            .expect("preserve standalone stage line");
        assert_eq!(
            reply.display_text.as_str(),
            "先检查网络。\n（轻轻点头）\n然后检查配置。"
        );
        assert_eq!(reply.speech_text.as_str(), "先检查网络。");

        let inline = ReplyCompiler
            .compile("先检查网络。然后确认配置（不要猜测）。", Vec::new())
            .expect("inline brackets remain valid");
        assert_eq!(
            inline.display_text.as_str(),
            "先检查网络。然后确认配置（不要猜测）。"
        );

        let psychological = ReplyCompiler
            .compile(
                "我听见了。\n（心里有些担心）\n你愿意再说一点吗？",
                Vec::new(),
            )
            .expect("preserve standalone psychological line");
        assert_eq!(
            psychological.display_text.as_str(),
            "我听见了。\n（心里有些担心）\n你愿意再说一点吗？"
        );
    }

    #[test]
    fn pure_bracketed_text_is_valid_but_other_invalid_speech_still_fails() {
        let bracketed = ReplyCompiler
            .compile("（安静地看着你）", Vec::new())
            .expect("bracketed text is allowed");
        assert_eq!(bracketed.display_text.as_str(), "（安静地看着你）");
        assert_eq!(bracketed.speech_text.as_str(), "（安静地看着你）");
        for invalid in [
            "看看 https://example.test。",
            "**加粗台词**",
            "我在这里🙂。",
        ] {
            assert!(ReplyCompiler.compile(invalid, Vec::new()).is_err());
        }
    }

    #[test]
    fn short_unpunctuated_line_is_still_speakable() {
        let reply = ReplyCompiler
            .compile("我在", Vec::new())
            .expect("compile short line");
        assert_eq!(reply.speech_text.as_str(), "我在");
    }
}
