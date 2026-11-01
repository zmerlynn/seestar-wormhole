async function checkNativeSetup(loop) {
 // Browser lookup returns null for IDs absent from the parsed markup.
 document.getElementById=id=>{
  const element=testElements.get(id);
  return element?.id===id?element:null;
 };
 for(const element of testElements.values()) {
  element.placeholder=element.attributes.placeholder||'';
  element.hasAttribute=name=>Object.prototype.hasOwnProperty.call(element.attributes,name);
 }
 const assert=(ok,message)=>{if(!ok)throw new Error(message);};
 eval(loop);
}
