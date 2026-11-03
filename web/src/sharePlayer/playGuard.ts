// 「这一次 play() 还算不算数」的凭据。
//
// 为什么需要它：`audio.play()` 是**异步**的，成功与否要到 Promise 落定才知道。
// 播放器在首屏会自动尝试一次播放（浏览器允许就直接播），于是从挂载到这次尝试
// 落定之间，存在一个几百毫秒的窗口：音频管线已经在走（currentTime 在涨），
// 但按钮文案还没切换。这段时间里按「暂停」是停不住的——`pause()` 先把
// playing 置回 false，随后 `play()` 的 then 回调又把 playing 置成 true 并重启
// 渲染循环。用户看到的是「点了暂停，过一会儿自己又播起来」；截图门禁看到的是
// 「明明请求的是 1967ms，拍下来的却是 1985ms」——差一帧，切点前后的判据就会
// 拍在另一张图上。
//
// 一个布尔量表达不了这件事：`playing=false` 既可能是「没人播」，也可能是
// 「有人播了但还没有结果」。凭据把「这次尝试」和「当前是否还有效」分开：
// 每次 play() 领一个号，pause() 或更新的 play() 都会让旧号作废；
// 落定后先 `accept(号)`，号过期就什么都不做。
//
// 之所以单独成文件：这是纯逻辑，可以在 node 里直接测（见 web/player-freeze-check.mjs）；
// 埋在 mount() 里就只能靠真机浏览器验，而这类竞态在真机上只偶尔出现。
export type PlayToken = number;

export type PlayGuard = {
  /** 开始一次新的播放尝试，返回它的凭据。 */
  begin(): PlayToken;
  /** 让所有在途尝试作废（暂停就是「刚才那次不算数」）。 */
  cancel(): void;
  /** 这次尝试的凭据是否仍然有效。 */
  accept(token: PlayToken): boolean;
};

export function createPlayGuard(): PlayGuard {
  let generation = 0;
  return {
    begin() {
      generation += 1;
      return generation;
    },
    cancel() {
      generation += 1;
    },
    accept(token) {
      return token === generation;
    }
  };
}
