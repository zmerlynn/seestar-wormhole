// A new controls document opens while the service still owns a failed save.
// The controller regression covers retaining that snapshot after a failed clear.
async function checkChangeTelescopeRetryAfterReload(nonempty) {
  const original = {email:'synthetic@example.invalid',serial:'synthetic-review-1234',model:'Future telescope',bind:'127.0.0.1'};
  const pair = nonempty ? {serial:'synthetic-retry-5678',model:'Saved telescope'} : {serial:'',model:''};
  const stopped = {active:false,status:'Stopped',settings:{...original,...pair},port_defaults:[],version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[]};
  // Bootstrap from the service, without a previous document's action history.
  document.hasFocus = () => false;
  window.addEventListener = () => {};
  api = async () => ({...stopped,settings:original,save_failed:true});
  await refresh();
  $('mode').value = 'idle'; $('idle').value = 7;
  api = async name => name === 'status' ? stopped : {ok:true};
  await act('retry-storage');
  if ($('serial').value !== pair.serial || $('model').value !== pair.model) throw new Error('reopened retry kept the old form pair instead of server settings');
  if ($('idle').value !== 7) throw new Error('retry overwrote an unrelated form draft');
  let submitted;
  api = async (name, body) => {if (name === 'start') submitted = body.settings; return name === 'status' ? stopped : {ok:true};};
  await act('start');
  if (!submitted || submitted.serial !== pair.serial || submitted.model !== pair.model) throw new Error('Connect did not submit the persisted pair after retry');
}
