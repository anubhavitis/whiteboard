import { useState } from "react";
import type { SkillsState } from "../agent/protocol";

interface Props {
  skills: SkillsState;
  /** Disabled mid-turn: changing skills restarts the agent. */
  disabled: boolean;
  onToggle: (enabled: string[]) => void;
  onSave: (id: string, body: string) => void;
  onDelete: (id: string) => void;
}

const NEW_SKILL_TEMPLATE = `# My skill

One line saying what this changes.

Then the guidance itself, in plain sentences. Say what to do and why — a rule with
a reason is followed more often than a bare instruction.`;

/** Turns a title into a filename-safe id, matching the server's validation. */
function toID(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

/**
 * The skills picker.
 *
 * Skills are additive: the core canvas skill is always applied and deliberately
 * absent from this list, because an agent without it emits raw pixels and quotes
 * shape ids — that reads as a bug, not a preference.
 *
 * Every enabled skill is resent on every turn, so the panel shows what the
 * selection costs against the canvas budget rather than hiding it.
 */
export function SkillsPanel({
  skills,
  disabled,
  onToggle,
  onSave,
  onDelete,
}: Props) {
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [draftName, setDraftName] = useState("");
  const [draftBody, setDraftBody] = useState("");

  const enabled = new Set(skills.enabled);
  const share = Math.round((skills.prompt_tokens / skills.canvas_budget) * 100);

  function toggle(id: string) {
    const next = new Set(enabled);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    onToggle([...next]);
  }

  function startNew() {
    setEditing("");
    setDraftName("");
    setDraftBody(NEW_SKILL_TEMPLATE);
  }

  function startEdit(id: string) {
    const skill = skills.skills.find((s) => s.id === id);
    if (!skill) return;
    setEditing(id);
    setDraftName(skill.name);
    setDraftBody(skill.body ?? "");
  }

  function save() {
    // An existing skill keeps its id: renaming the file would orphan the
    // selection that refers to it.
    const id = editing || toID(draftName);
    if (!id || !draftBody.trim()) return;
    onSave(id, draftBody);
    setEditing(null);
  }

  if (editing !== null) {
    return (
      <div className="skills skills--editing">
        <div className="skills__header">
          <strong>{editing ? "Edit skill" : "New skill"}</strong>
          <button className="skills__link" onClick={() => setEditing(null)}>
            Cancel
          </button>
        </div>
        {!editing && (
          <input
            className="skills__name"
            value={draftName}
            onChange={(e) => setDraftName(e.target.value)}
            placeholder="Skill name"
          />
        )}
        <textarea
          className="skills__body"
          value={draftBody}
          onChange={(e) => setDraftBody(e.target.value)}
          rows={12}
          spellCheck
        />
        <div className="skills__footer">
          <span className="skills__hint">
            {editing ? `saved as ${editing}.md` : draftName && `saved as ${toID(draftName)}.md`}
          </span>
          <button
            onClick={save}
            disabled={!draftBody.trim() || (!editing && !draftName.trim())}
          >
            Save
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="skills">
      <button
        className="skills__toggle"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
      >
        <span>Skills</span>
        <span className="skills__count">
          {skills.enabled.length > 0 ? `${skills.enabled.length} on` : "none"}
          <span className="skills__chevron">{open ? "▾" : "▸"}</span>
        </span>
      </button>

      {open && (
        <div className="skills__list">
          <p className="skills__note">
            Canvas reading and drawing rules always apply. These add to them.
          </p>

          {skills.skills.map((skill) => (
            <label
              key={skill.id}
              className={`skills__item${disabled ? " skills__item--disabled" : ""}`}
            >
              <input
                type="checkbox"
                checked={enabled.has(skill.id)}
                onChange={() => toggle(skill.id)}
                disabled={disabled}
              />
              <span className="skills__text">
                <span className="skills__title">
                  {skill.name}
                  <span className="skills__tokens">~{skill.tokens} tok</span>
                </span>
                <span className="skills__desc">{skill.description}</span>
              </span>
              {!skill.built_in && (
                <span className="skills__actions">
                  <button
                    className="skills__link"
                    onClick={(e) => {
                      e.preventDefault();
                      startEdit(skill.id);
                    }}
                  >
                    Edit
                  </button>
                  <button
                    className="skills__link skills__link--danger"
                    onClick={(e) => {
                      e.preventDefault();
                      onDelete(skill.id);
                    }}
                  >
                    Delete
                  </button>
                </span>
              )}
            </label>
          ))}

          <div className="skills__footer">
            <span
              className="skills__hint"
              title="Every enabled skill is sent again on each turn, competing with the canvas for context."
            >
              prompt ~{skills.prompt_tokens} tok · {share}% of the canvas budget
            </span>
            <button className="skills__link" onClick={startNew}>
              + New skill
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
