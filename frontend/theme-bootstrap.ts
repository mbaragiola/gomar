// Fixed first-paint script. Keep it independent of anything in an API response.
export const themeKey = 'gomar.theme'

export const themeBootstrap = `(()=>{try{const s=localStorage.getItem(${JSON.stringify(themeKey)});if(s==="light"||s==="dark")document.documentElement.dataset.theme=s}catch{}})();`
