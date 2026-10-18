<script lang="ts">
  import { onMount } from "svelte";
  import { Compartment, EditorState } from "@codemirror/state";
  import { EditorView } from "@codemirror/view";
  import { basicSetup } from "codemirror";
  import { yaml } from "@codemirror/lang-yaml";
  import { oneDark } from "@codemirror/theme-one-dark";
  import { isDarkMode } from "../../stores/theme";

  interface Props {
    value: string;
    onchange?: (value: string) => void;
  }

  let { value = $bindable(""), onchange }: Props = $props();

  let host: HTMLDivElement;
  let view: EditorView | undefined;
  let syncing = false;
  const theme = new Compartment();

  onMount(() => {
    view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: value,
        extensions: [
          basicSetup,
          yaml(),
          theme.of($isDarkMode ? oneDark : []),
          EditorView.updateListener.of((update) => {
            if (!update.docChanged) return;
            syncing = true;
            value = update.state.doc.toString();
            syncing = false;
            onchange?.(value);
          }),
        ],
      }),
    });
    return () => view?.destroy();
  });

  $effect(() => {
    view?.dispatch({ effects: theme.reconfigure($isDarkMode ? oneDark : []) });
  });

  $effect(() => {
    const next = value;
    if (!view || syncing) return;
    if (view.state.doc.toString() !== next) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: next } });
    }
  });
</script>

<div
  bind:this={host}
  class="border-input bg-background w-full overflow-hidden rounded-md border"
  style="height: clamp(28rem, 65vh, 52rem);"
></div>

<style>
  :global(.cm-editor) {
    height: 100%;
    font-size: 0.875rem;
  }
  :global(.cm-scroller) {
    overflow: auto;
  }
  :global(.cm-editor.cm-focused) {
    outline: none;
  }
</style>
