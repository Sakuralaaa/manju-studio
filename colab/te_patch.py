# 漫剧工坊：给新版 ComfyUI 的 MiniMax H3 模型加 TE-Speed 需要的 block_loop 钩子。
# TE-Speed-MiniMaxH3-OSS 自带的 patch_model.py 只认 2026 年 8 月初的 model.py；
# 之后 ComfyUI 把块循环搬进了 _run_blocks(h, t_emb, mod_segments, layout, transformer_options)，
# 这里按新结构做同样两件事：块区间执行（start/end）和 ("block_loop", 0) 钩子。
# 用法：python te_patch.py <ComfyUI 根目录> [--revert]；成功打印 OK，已打过打印 ALREADY。
import re, sys, shutil
from pathlib import Path

MARK = "# mj-te-speed hook"
OLD_DEF = "    def _run_blocks(self, h, t_emb, mod_segments, layout, transformer_options):\n"

HOOK = '''    def _run_blocks(self, h, t_emb, mod_segments, layout, transformer_options):  ''' + MARK + '''
        blocks_replace = transformer_options.get("patches_replace", {}).get("dit", {})
        if ("block_loop", 0) in blocks_replace:
            def block_loop_wrap(args):
                return {"img": self._run_blocks_range(args["img"], args["t_emb"], args["mod_segments"], layout,
                                                      args["transformer_options"], args.get("start", 0), args.get("end"))}
            cache_ranges = [(a, b) for a, b, kind in layout.segments if kind in ("audio", "video")]
            return blocks_replace[("block_loop", 0)](
                {"img": h, "t_emb": t_emb, "mod_segments": mod_segments, "transformer_options": transformer_options,
                 "cache_ranges": cache_ranges, "block_count": len(self.blocks)},
                {"original_block": block_loop_wrap})["img"]
        return self._run_blocks_range(h, t_emb, mod_segments, layout, transformer_options)

    def _run_blocks_range(self, h, t_emb, mod_segments, layout, transformer_options, start=0, end=None):
'''


def patch(text):
    if MARK in text:
        return None
    i = text.find(OLD_DEF)
    if i < 0:
        raise SystemExit("找不到 _run_blocks，ComfyUI 版本不匹配")
    body_start = i + len(OLD_DEF)
    j = text.find("\n    def ", body_start)
    body = text[body_start:j]
    loop = "        for i, block in enumerate(self.blocks):\n"
    pq = "comfy.model_prefetch.make_prefetch_queue(list(self.blocks), device, transformer_options)"
    if body.count(loop) != 1 or body.count(pq) != 1:
        raise SystemExit("_run_blocks 的块循环和预想的不一样，ComfyUI 版本不匹配")
    body = body.replace(pq, "comfy.model_prefetch.make_prefetch_queue(list(self.blocks[start:end]), device, transformer_options)")
    body = body.replace(loop, "        end = len(self.blocks) if end is None else end\n        for i in range(start, end):\n            block = self.blocks[i]\n")
    return text[:i] + HOOK + body + text[j:]


def main():
    root = Path(sys.argv[1])
    f = root / "comfy" / "ldm" / "minimax" / "model.py"
    bak = f.with_name(f.name + ".mj_te.bak")
    if "--revert" in sys.argv:
        if bak.exists():
            shutil.copy(bak, f)
        print("REVERTED")
        return
    text = f.read_text(encoding="utf-8")
    out = patch(text)
    if out is None:
        print("ALREADY")
        return
    compile(out, str(f), "exec")
    if not bak.exists():
        shutil.copy(f, bak)
    f.write_text(out, encoding="utf-8")
    print("OK")


if __name__ == "__main__":
    main()
