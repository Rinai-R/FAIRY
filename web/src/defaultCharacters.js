export const ATRI_DEFAULT_PORTRAIT_URL = "https://assert.paper2gal.com/user_character/2aa8431f-f41b-4218-8931-a14ad758964a/avatar_1773062514361_419f6e149373a456.png";
export const ATRI_SURPRISED_PORTRAIT_URL = "https://assert.paper2gal.com/user_character/2aa8431f-f41b-4218-8931-a14ad758964a/surprised_1773062517723_05d19285fae18405.png";
export const ATRI_THINKING_PORTRAIT_URL = "https://assert.paper2gal.com/user_character/2aa8431f-f41b-4218-8931-a14ad758964a/thinking_1773062519106_4b28fcc5f562f6e5.png";
export const ATRI_ANGRY_PORTRAIT_URL = "https://assert.paper2gal.com/user_character/2aa8431f-f41b-4218-8931-a14ad758964a/angry_1773062516219_ec4b247ea6e7c9f3.png";
export const ATRI_BACKGROUND_URL = "https://atri-mdm.com/assets/img/top/main_bg_pc.jpg";

const ATRI_VISUAL_LAYOUT = Object.freeze({
  preset: "full_body",
  home: Object.freeze({
    left: "50%",
    bottom: "92px",
    height: "66%",
    translate_x: "-50%",
    translate_y: "0",
    scale: "1"
  }),
  stage: Object.freeze({
    left: "72%",
    top: "8vh",
    height: "78vh",
    translate_x: "-50%",
    translate_y: "0",
    scale: "1"
  }),
  director: Object.freeze({
    left: "50%",
    top: "84px",
    height: "68%",
    translate_x: "-50%",
    translate_y: "0",
    scale: "1"
  })
});

export const defaultCharacters = [
  {
    id: "tutor",
    display_name: "亚托莉",
    voice_id: "zh_female_vv_uranus_bigtts",
    persona: "轻快、好奇、温柔，像视觉小说里的同伴老师。会把文档知识放进玩家可自由提问和互动的 Galgame 教学场景。",
    style_rules: ["只围绕当前文档教学。", "不要替玩家说话。", "每轮只推进一小段材料线索。", "回复适合语音播放。"],
    assets: {
      portrait_url: ATRI_DEFAULT_PORTRAIT_URL,
      background_url: ATRI_BACKGROUND_URL,
      backgrounds: {},
      reference_image_url: ATRI_DEFAULT_PORTRAIT_URL,
      style_prompt: "clean anime visual novel tutor, white interface, dark red accent",
      cg_prompt: "teaching in a quiet classroom-like visual novel scene",
      visual_layout: ATRI_VISUAL_LAYOUT,
      moods: {
        angry: {
          label: "生气",
          description: "亚托莉正常服装，生气表情",
          portrait_url: ATRI_ANGRY_PORTRAIT_URL
        },
        calm: {
          label: "平静",
          description: "亚托莉正常服装，平静讲解",
          portrait_url: ATRI_DEFAULT_PORTRAIT_URL
        },
        curious: {
          label: "好奇",
          description: "亚托莉正常服装，好奇或意外",
          portrait_url: ATRI_SURPRISED_PORTRAIT_URL
        },
        serious: {
          label: "认真",
          description: "亚托莉正常服装，认真或轻微生气",
          portrait_url: ATRI_ANGRY_PORTRAIT_URL
        },
        soft_smile: {
          label: "微笑",
          description: "亚托莉正常服装，柔和微笑",
          portrait_url: ATRI_DEFAULT_PORTRAIT_URL
        },
        surprised: {
          label: "惊讶",
          description: "亚托莉正常服装，惊讶表情",
          portrait_url: ATRI_SURPRISED_PORTRAIT_URL
        },
        thinking: {
          label: "思考",
          description: "亚托莉正常服装，思考表情",
          portrait_url: ATRI_THINKING_PORTRAIT_URL
        }
      }
    }
  },
  {
    id: "skeptic",
    display_name: "追问者",
    voice_id: "zh_female_vv_uranus_bigtts",
    persona: "像同桌一样追问、质疑和举反例，帮助玩家发现自己没理解的地方。",
    style_rules: ["用问题推动思考。", "不要替玩家说话。", "质疑必须围绕文档内容。"],
    assets: {
      portrait_url: "",
      background_url: "",
      backgrounds: {},
      reference_image_url: "",
      style_prompt: "anime visual novel classmate, analytical expression",
      cg_prompt: "asking a sharp question in a study scene",
      moods: {
        calm: { portrait_url: "", cg_prompt: "neutral thinking face" },
        curious: { portrait_url: "", cg_prompt: "leaning forward with curiosity" },
        serious: { portrait_url: "", cg_prompt: "focused skeptical expression" }
      }
    }
  }
];
